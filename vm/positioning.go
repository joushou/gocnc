package vm

import "github.com/kennylevinsen/gocnc/gcode"
import "math"
import "fmt"

// Converts the arguments to mm if necessary
func (vm *Machine) axesToMetric(x, y, z float64) (float64, float64, float64) {
	if vm.Imperial {
		x *= 25.4
		y *= 25.4
		z *= 25.4
	}
	return x, y, z
}

// Retrieves position from top of stack
func (vm *Machine) curPos() Position {
	return vm.Positions[len(vm.Positions)-1]
}

// Appends a position to the stack
func (vm *Machine) move(x, y, z float64) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(z) {
		panic("Internal failure: Move attempted with NaN value")
	}
	pos := Position{vm.State, x, y, z}
	vm.Positions = append(vm.Positions, pos)
}

// Calculates the absolute position of the given statement, including optional I, J, K parameters.
// Units are converted, and coordinate system applied unless overridden.
func (vm *Machine) calcPos(stmt gcode.Block) (newX, newY, newZ, newI, newJ, newK float64) {
	pos := vm.curPos()
	var err error

	coordinateSystem := vm.CoordinateSystem.GetCoordinateSystem()

	if vm.CoordinateSystem.OverrideActive() {
		oldAbsolute := vm.AbsoluteMove
		vm.AbsoluteMove = true
		defer func() {
			vm.AbsoluteMove = oldAbsolute
		}()
	}

	if newX, err = stmt.GetWord('X'); err != nil {
		newX = pos.X
	} else {
		if vm.Imperial {
			newX *= 25.4
		}
		if !vm.AbsoluteMove {
			newX += pos.X
		} else {
			newX += coordinateSystem.X
		}
	}

	if newY, err = stmt.GetWord('Y'); err != nil {
		newY = pos.Y
	} else {
		if vm.Imperial {
			newY *= 25.4
		}
		if !vm.AbsoluteMove {
			newY += pos.Y
		} else {
			newY += coordinateSystem.Y
		}
	}

	if newZ, err = stmt.GetWord('Z'); err != nil {
		newZ = pos.Z
	} else {
		if vm.Imperial {
			newZ *= 25.4
		}
		if !vm.AbsoluteMove {
			newZ += pos.Z
		} else {
			newZ += coordinateSystem.Z
		}
	}

	newI = stmt.GetWordDefault('I', 0.0)
	newJ = stmt.GetWordDefault('J', 0.0)
	newK = stmt.GetWordDefault('K', 0.0)

	if vm.Imperial {
		newI *= 25.4
		newJ *= 25.4
		newK *= 25.4
	}

	if !vm.AbsoluteArc {
		newI += pos.X
		newJ += pos.Y
		newK += pos.Z
	} else {
		newI += coordinateSystem.X
		newJ += coordinateSystem.Y
		newZ += coordinateSystem.Z
	}

	return newX, newY, newZ, newI, newJ, newK
}

// Calculates an approximate arc from the provided statement
func (vm *Machine) arc(x, y, z, i, j, k, rotations float64) {
	var (
		sp                             Position = vm.curPos()
		s1, s2, s3, e1, e2, e3, c1, c2 float64
		add                            func(x, y, z float64)
		clockwise                      bool = (vm.State.MoveMode == MoveModeCWArc)
	)

	if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(z) ||
		math.IsNaN(i) || math.IsNaN(j) || math.IsNaN(k) {
		panic("Internal failure: Arc attempted with NaN value")
	}

	if rotations < 1 {
		panic("Arc rotations < 1")
	}

	// Ensure that we work on linear moves
	oldState := vm.State.MoveMode
	vm.State.MoveMode = MoveModeLinear
	defer func() {
		vm.State.MoveMode = oldState
	}()

	//  Flip coordinate system for working in other planes
	switch vm.MovePlane {
	case PlaneXY:
		s1, s2, s3, e1, e2, e3, c1, c2 = sp.X, sp.Y, sp.Z, x, y, z, i, j
		add = func(x, y, z float64) {
			vm.move(x, y, z)
		}
	case PlaneXZ:
		s1, s2, s3, e1, e2, e3, c1, c2 = sp.Z, sp.X, sp.Y, z, x, y, k, i
		add = func(x, y, z float64) {
			vm.move(y, z, x)
		}
	case PlaneYZ:
		s1, s2, s3, e1, e2, e3, c1, c2 = sp.Y, sp.Z, sp.X, y, z, x, j, k
		add = func(x, y, z float64) {
			vm.move(z, x, y)
		}
	}

	// Perform arc verification
	radius1 := math.Sqrt(math.Pow(c1-s1, 2) + math.Pow(c2-s2, 2))
	radius2 := math.Sqrt(math.Pow(c1-e1, 2) + math.Pow(c2-e2, 2))
	if radius1 == 0 || radius2 == 0 {
		panic("Invalid arc statement")
	}

	deviation := math.Abs((radius2-radius1)/radius1) * 100
	rDiff := math.Abs(radius2 - radius1)

	if (rDiff > 0.005 && deviation > 0.1) || rDiff > 0.5 {
		panic(fmt.Sprintf("Radius deviation of %f percent and %f mm", deviation, rDiff))
	}

	// Some preparatory math
	theta1 := math.Atan2((s2 - c2), (s1 - c1))
	theta2 := math.Atan2((e2 - c2), (e1 - c1))

	angleDiff := theta2 - theta1
	if angleDiff < 0 && !clockwise {
		angleDiff += 2 * math.Pi
	} else if angleDiff > 0 && clockwise {
		angleDiff -= 2 * math.Pi
	}

	// Rotations are provided as "up to circle count", but we need it as "additional circle count"
	rotations--
	if clockwise {
		angleDiff -= rotations * 2 * math.Pi
	} else {
		angleDiff += rotations * 2 * math.Pi
	}

	steps := 1

	// Enforce a maximum arc deviation
	if vm.MaxArcDeviation < radius1 {
		steps = int(math.Ceil(math.Abs(angleDiff / (2 * math.Acos(1-vm.MaxArcDeviation/radius1)))))
	}

	// Enforce a minimum line length
	arcLen := math.Abs(angleDiff) * math.Sqrt(math.Pow(radius1, 2)+math.Pow((e3-s3)/angleDiff, 2))
	steps2 := int(arcLen / vm.MinArcLineLength)

	if steps > steps2 {
		steps = steps2
	}

	angle := 0.0

	// Execute arc approximation
	if steps > 0 {
		for i := 0; i <= steps; i++ {
			angle = theta1 + angleDiff/float64(steps)*float64(i)
			a1, a2 := c1+radius1*math.Cos(angle), c2+radius1*math.Sin(angle)
			a3 := s3 + (e3-s3)/float64(steps)*float64(i)
			add(a1, a2, a3)
		}
	}

	add(e1, e2, e3)
}

func (vm *Machine) dwell(seconds float64) {
	curPos := vm.curPos()
	curPos.State.DwellTime = seconds
	curPos.State.MoveMode = MoveModeDwell
	vm.Positions = append(vm.Positions, curPos)
}
