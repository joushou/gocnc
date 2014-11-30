package vm

import "github.com/joushou/gocnc/utils"

import "math"
import "errors"
import "fmt"

//
// Ideas for other optimization steps:
//   Move grouping - Group moves based on Z0, Zdepth lifts, to finalize
//      section, instead of constantly moving back and forth
//   Vector-angle removal - Combine moves where the move vector changes
//      less than a certain minimum angle
//

// Detects a previous drill, and uses rapid move to the previous known depth.
// Scans through all Z-descent moves, logs its height, and ensures that any future move
// at that location will use MoveModeRapid to go to the deepest previous known Z-height.
func (vm *Machine) OptDrillSpeed() {
	var (
		lastx, lasty, lastz float64
		npos                []Position = make([]Position, 0)
		drillStack          []Position = make([]Position, 0)
	)

	fastDrill := func(pos Position) (Position, Position, bool) {
		var depth float64
		var found bool
		for _, m := range drillStack {
			if m.X == pos.X && m.Y == pos.Y {
				if m.Z < depth {
					depth = m.Z
					found = true
				}
			}
		}

		drillStack = append(drillStack, pos)

		if found {
			if pos.Z >= depth { // We have drilled all of it, so just rapid all the way
				pos.State.MoveMode = MoveModeRapid
				return pos, pos, false
			} else { // Can only rapid some of the way
				p := pos
				p.Z = depth
				p.State.MoveMode = MoveModeRapid
				return p, pos, true
			}
		} else {
			return pos, pos, false
		}
	}

	for _, m := range vm.Positions {
		if m.X == lastx && m.Y == lasty && m.Z < lastz && m.State.MoveMode == MoveModeLinear {
			posn, poso, shouldinsert := fastDrill(m)
			if shouldinsert {
				npos = append(npos, posn)
			}
			npos = append(npos, poso)
		} else {
			npos = append(npos, m)
		}
		lastx, lasty, lastz = m.X, m.Y, m.Z
	}
	vm.Positions = npos
}

// Reduces moves between routing operations.
// It does this by scanning through position stack, grouping moves that move from >= Z0 to < Z0.
// These moves are then sorted after closest to previous position, starting at X0 Y0,
// and moves to groups recalculated as they are inserted in a new stack.
// This optimization pass bails if the Z axis is moved simultaneously with any other axis,
// or the input ends with the drill below Z0, in order to play it safe.
// This pass is new, and therefore slightly experimental.
func (vm *Machine) OptRouteGrouping(tolerance float64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprintf("%s", r))
		}
	}()

	type Set []Position
	var (
		lastx, lasty, lastz float64
		sets                []Set = make([]Set, 0)
		curSet              Set   = make(Set, 0)
		safetyHeight        float64
		drillSpeed          float64
		sequenceStarted     bool = false
	)

	// Find grouped drills
	for _, m := range vm.Positions {
		if m.Z != lastz && (m.X != lastx || m.Y != lasty) {
			panic("Complex z-motion detected")
		}

		if m.X == lastx && m.Y == lasty {
			if lastz >= 0 && m.Z < 0 {
				// Down move
				sequenceStarted = true

				// Set drill feedrate
				if m.State.MoveMode == MoveModeLinear && m.State.Feedrate > drillSpeed {
					if drillSpeed != 0 {
						panic("Multiple drill feedrates detected")
					}
					drillSpeed = m.State.Feedrate
				}
			} else if lastz < 0 && m.Z >= 0 {
				// Up move - ignored in set
				//curSet = append(curSet, m)
				if sequenceStarted {
					sets = append(sets, curSet)
				}
				sequenceStarted = false
				curSet = make(Set, 0)
				goto updateLast // Skip append
			}

		} else {
			if m.Z < 0 && m.State.MoveMode == MoveModeRapid {
				panic("Rapid move in stock detected")
			}
		}

		if sequenceStarted {
			// Regular move
			if m.Z > 0 {
				panic("Move above stock detected")
			}
			curSet = append(curSet, m)
		}

	updateLast:
		if m.Z > safetyHeight {
			safetyHeight = m.Z
		}
		lastx, lasty, lastz = m.X, m.Y, m.Z
	}

	if safetyHeight == 0 {
		panic("Unable to detect safety height")
	} else if drillSpeed == 0 {
		panic("Unable to detect drill feedrate")
	}

	// If there was a final set without a proper lift
	if len(curSet) == 1 {
		p := curSet[0]
		if p.Z != safetyHeight || lastz != safetyHeight || p.X != 0 || p.Y != 0 {
			panic("Incomplete final drill set")
		}
	} else if len(curSet) > 0 {
		panic("Incomplete final drill set")
	}

	var (
		curVec      utils.Vector
		sortedSets  []Set = make([]Set, 0)
		selectedSet int
	)

	// Stupid difference calculator
	xyDiff := func(pos utils.Vector, cur utils.Vector) float64 {
		j := cur.Diff(pos)
		j.Z = 0
		return j.Norm()
	}

	// Sort the sets after distance from current position
	for len(sets) > 0 {
		for idx, _ := range sets {
			if selectedSet == -1 {
				selectedSet = idx
			} else {
				np := sets[idx][0]
				pp := sets[selectedSet][0]
				diff := xyDiff(np.Vector(), curVec)
				other := xyDiff(pp.Vector(), curVec)
				if diff < other {
					selectedSet = idx
				} else if np.Z > pp.Z {
					selectedSet = idx
				}
			}
		}
		curVec = sets[selectedSet][0].Vector()
		sortedSets = append(sortedSets, sets[selectedSet])
		sets = append(sets[0:selectedSet], sets[selectedSet+1:]...)
		selectedSet = -1
	}

	// Reconstruct new position stack from sorted sections
	newPos := []Position{vm.Positions[0]} // Origin

	addPos := func(pos Position) {
		newPos = append(newPos, pos)
	}

	moveTo := func(pos Position) {
		curPos := newPos[len(newPos)-1]

		// Check if we should go to safety-height before moving
		if xyDiff(curPos.Vector(), pos.Vector()) < tolerance {
			if curPos.X != pos.X || curPos.Y != pos.Y {
				// If we're not 100% precise...
				step1 := curPos
				step1.State.MoveMode = MoveModeLinear
				step1.X = pos.X
				step1.Y = pos.Y
				addPos(step1)
			}
			addPos(pos)
		} else {
			step1 := curPos
			step1.Z = safetyHeight
			step1.State.MoveMode = MoveModeRapid
			step2 := step1
			step2.X, step2.Y = pos.X, pos.Y
			step3 := step2
			step3.Z = pos.Z
			step3.State.MoveMode = MoveModeLinear
			step3.State.Feedrate = drillSpeed

			addPos(step1)
			addPos(step2)
			addPos(step3)
		}

	}

	for _, m := range sortedSets {
		for idx, p := range m {
			if idx == 0 {
				moveTo(p)
			} else {
				addPos(p)
			}
		}
	}

	vm.Positions = newPos

	return nil
}

// Uses rapid move for all Z-up only moves.
// Scans all positions for moves that only change the z-axis in a positive direction,
// and sets the moveMode to MoveModeRapid.
func (vm *Machine) OptLiftSpeed() {
	var lastx, lasty, lastz float64
	for idx, m := range vm.Positions {
		if m.X == lastx && m.Y == lasty && m.Z > lastz {
			// We got a lift! Let's make it faster, shall we?
			vm.Positions[idx].State.MoveMode = MoveModeRapid
		}
		lastx, lasty, lastz = m.X, m.Y, m.Z
	}
}

// Kills redundant partial moves.
// Calculates the unit-vector, and kills all incremental moves between A and B.
// Deprecated by OptVector.
func (vm *Machine) OptBogusMoves() {
	var (
		xstate, ystate, zstate       float64
		vecX, vecY, vecZ             float64
		lastvecX, lastvecY, lastvecZ float64
		npos                         []Position = make([]Position, 0)
	)

	for _, m := range vm.Positions {
		dx, dy, dz := m.X-xstate, m.Y-ystate, m.Z-zstate
		xstate, ystate, zstate = m.X, m.Y, m.Z

		if m.State.MoveMode != MoveModeRapid && m.State.MoveMode != MoveModeLinear {
			lastvecX, lastvecY, lastvecZ = 0, 0, 0
			continue
		}

		if dx == 0 && dz == 0 && dy == 0 {
			// Why are we doing this again?!
			continue
		}

		norm := math.Sqrt(math.Pow(dx, 2) + math.Pow(dy, 2) + math.Pow(dz, 2))
		vecX, vecY, vecZ = dx/norm, dy/norm, dz/norm

		if lastvecX == vecX && lastvecY == vecY && lastvecZ == vecZ {
			npos[len(npos)-1] = m
		} else {
			npos = append(npos, m)
			lastvecX, lastvecY, lastvecZ = vecX, vecY, vecZ
		}
	}
	vm.Positions = npos
}

// Kills redundant partial moves.
// Calculates the unit-vector, and kills all incremental moves between A and B.
func (vm *Machine) OptVector(tolerance float64) {
	var (
		vec1, vec2, vec3 utils.Vector
		ready            int
		length1, length2 float64
		lastMoveMode     int
		npos             []Position = make([]Position, 0)
	)

	for _, m := range vm.Positions {
		if m.State.MoveMode != MoveModeLinear && m.State.MoveMode != MoveModeRapid {
			ready = 0
			goto appendpos
		}

		if m.State.MoveMode != lastMoveMode {
			lastMoveMode = m.State.MoveMode
			ready = 0
		}

		if ready == 0 {
			vec1 = m.Vector()
			ready++
			goto appendpos
		} else if ready == 1 {
			vec2 = m.Vector()
			ready++
			goto appendpos
		} else if ready == 2 {
			vec3 = m.Vector()
			ready++
		} else {
			vec1 = vec2
			vec2 = vec3
			vec3 = m.Vector()
		}

		length1 = vec1.Diff(vec2).Norm() + vec2.Diff(vec3).Norm()
		length2 = vec1.Diff(vec3).Norm()
		if length1-length2 < tolerance {
			npos[len(npos)-1] = m
			vec2 = vec1
			continue
		}

	appendpos:
		npos = append(npos, m)
	}
	vm.Positions = npos
}
