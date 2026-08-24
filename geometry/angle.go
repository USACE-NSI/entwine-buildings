// geometry/angle.go
package geometry

import "math"

// NormalizeOrientDeg folds a PCA/ridge angle (rad, any range) into 0..180 deg.
func NormalizeOrientDeg(a float64) float64 {
	d := math.Mod(a*180/math.Pi, 180)
	if d < 0 {
		d += 180
	}
	return d
}
