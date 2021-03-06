package vm

import "errors"
import "fmt"
import "math"
import "time"

// Flips the X and Y axes of all moves
func (vm *Machine) FlipXY() {
	for idx := range vm.Positions {
		pos := vm.Positions[idx]
		vm.Positions[idx].X, vm.Positions[idx].Y = pos.Y, pos.X
	}
}

// Limit feedrate.
func (vm *Machine) LimitFeedrate(feed float64) {
	for idx, m := range vm.Positions {
		if m.State.Feedrate > feed {
			vm.Positions[idx].State.Feedrate = feed
		}
	}
}

// Increase feedrate
func (vm *Machine) FeedrateMultiplier(feedMultiplier float64) {
	for idx := range vm.Positions {
		vm.Positions[idx].State.Feedrate *= feedMultiplier
	}
}

// Multiply move distances - This makes no sense - Dangerous.
func (vm *Machine) MoveMultiplier(moveMultiplier float64) {
	for idx := range vm.Positions {
		vm.Positions[idx].X *= moveMultiplier
		vm.Positions[idx].Y *= moveMultiplier
		vm.Positions[idx].Z *= moveMultiplier
	}
}

// Enforce spindle mode
func (vm *Machine) EnforceSpindle(enabled, clockwise bool, speed float64) {
	for idx := range vm.Positions {
		vm.Positions[idx].State.SpindleSpeed = speed
		vm.Positions[idx].State.SpindleEnabled = enabled
		vm.Positions[idx].State.SpindleClockwise = clockwise
	}
}

// Detect the highest Z position
func (vm *Machine) FindSafetyHeight() float64 {
	var maxz float64
	for _, m := range vm.Positions {
		if m.Z > maxz {
			maxz = m.Z
		}
	}
	return maxz
}

// Set safety-height.
// Scans for the highest position on the Y axis, and afterwards replaces all instances
// of this position with the requested height.
func (vm *Machine) SetSafetyHeight(height float64) error {
	// Ensure we detected the highest point in the script - we don't want any collisions

	maxz := vm.FindSafetyHeight()
	nextz := 0.0
	for _, m := range vm.Positions {
		if m.Z < maxz && m.Z > nextz {
			nextz = m.Z
		}
	}

	if height <= nextz {
		return errors.New(fmt.Sprintf("New safety height collides with lower feed height of %g", nextz))
	}

	// Apply the changes
	var lastx, lasty float64
	for idx, m := range vm.Positions {
		if lastx == m.X && lasty == m.Y && m.Z == maxz {
			vm.Positions[idx].Z = height
		}
		lastx, lasty = m.X, m.Y
	}
	return nil
}

// Ensure return to X0 Y0 Z0.
// Simply adds a what is necessary to move back to X0 Y0 Z0.
func (vm *Machine) Return(disableSpindle, disableCoolant bool) {
	var maxz float64
	for _, m := range vm.Positions {
		if m.Z > maxz {
			maxz = m.Z
		}
	}
	if len(vm.Positions) == 0 {
		return
	}
	lastPos := vm.Positions[len(vm.Positions)-1]
	if lastPos.X == 0 && lastPos.Y == 0 && lastPos.Z == 0 {
		if disableSpindle {
			lastPos.State.SpindleEnabled = false
		}
		if disableCoolant {
			lastPos.State.MistCoolant = false
			lastPos.State.FloodCoolant = false
		}
		vm.Positions[len(vm.Positions)-1] = lastPos
		return
	} else if lastPos.X == 0 && lastPos.Y == 0 && lastPos.Z != 0 {
		lastPos.Z = 0
		lastPos.State.MoveMode = MoveModeRapid
		if disableSpindle {
			lastPos.State.SpindleEnabled = false
		}
		if disableCoolant {
			lastPos.State.MistCoolant = false
			lastPos.State.FloodCoolant = false
		}
		vm.Positions = append(vm.Positions, lastPos)
		return
	} else if lastPos.Z == maxz {
		move1 := lastPos
		move1.X = 0
		move1.Y = 0
		move1.State.MoveMode = MoveModeRapid
		move2 := move1
		move2.Z = 0
		if disableSpindle {
			move2.State.SpindleEnabled = false
		}
		if disableCoolant {
			move2.State.MistCoolant = false
			move2.State.FloodCoolant = false
		}
		vm.Positions = append(vm.Positions, move1)
		vm.Positions = append(vm.Positions, move2)
		return
	} else {
		move1 := lastPos
		move1.Z = maxz
		move1.State.MoveMode = MoveModeRapid
		move2 := move1
		move2.X = 0
		move2.Y = 0
		move3 := move2
		move3.Z = 0
		if disableSpindle {
			move3.State.SpindleEnabled = false
		}
		if disableCoolant {
			move3.State.MistCoolant = false
			move3.State.FloodCoolant = false
		}
		vm.Positions = append(vm.Positions, move1)
		vm.Positions = append(vm.Positions, move2)
		vm.Positions = append(vm.Positions, move3)
		return
	}
}

// Generate move information
func (vm *Machine) Info() (minx, miny, minz, maxx, maxy, maxz float64, feedrates []float64) {
	for _, pos := range vm.Positions {
		if pos.X < minx {
			minx = pos.X
		} else if pos.X > maxx {
			maxx = pos.X
		}

		if pos.Y < miny {
			miny = pos.Y
		} else if pos.Y > maxy {
			maxy = pos.Y
		}

		if pos.Z < minz {
			minz = pos.Z
		} else if pos.Z > maxz {
			maxz = pos.Z
		}

		feedrateFound := false
		for _, feed := range feedrates {
			if feed == pos.State.Feedrate {
				feedrateFound = true
				break
			}
		}
		if !feedrateFound {
			feedrates = append(feedrates, pos.State.Feedrate)
		}
	}
	return
}

// Estimate runtime for job
func (m *Machine) ETA() time.Duration {
	lastTool := -1
	lastToolSuggestion := -1
	var eta time.Duration
	var lx, ly, lz float64
	for _, pos := range m.Positions {
		if pos.State.ToolIndex != lastTool {
			if pos.State.ToolIndex == lastToolSuggestion {
				eta += 5 * time.Second
			} else {
				eta += 10 * time.Second
			}
		}
		lastTool = pos.State.ToolIndex
		lastToolSuggestion = pos.State.NextToolIndex

		feed := pos.State.Feedrate
		if feed <= 0 {
			// Just to use something...
			feed = 300
		}

		// Convert from minutes to microseconds
		feed /= 60000000

		switch pos.State.MoveMode {
		case MoveModeNone:
			continue
		case MoveModeRapid:
			// This is silly, but it gives something to calculate with
			feed *= 8
		case MoveModeDwell:
			eta += time.Duration(pos.State.DwellTime) * time.Second
			continue
		}
		dx, dy, dz := pos.X-lx, pos.Y-ly, pos.Z-lz
		lx, ly, lz = pos.X, pos.Y, pos.Z

		dist := math.Sqrt(math.Pow(dx, 2) + math.Pow(dy, 2) + math.Pow(dz, 2))
		eta += time.Duration(dist/feed) * time.Microsecond
	}
	return eta
}
