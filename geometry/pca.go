// geometry/pca.go
package geometry

import "math"

// PcaAngle returns the orientation (rad) of the principal axis, the
// centroid, and the eigenvalue ratio (>=1) of a 2-D point set.
// ratio ~1 => near-square.
func PcaAngle(pts [][2]float64) (angle, cx, cy, evRatio float64) {
	n := len(pts)
	if n == 0 {
		return
	}
	cx, cy = 0, 0
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	cx /= float64(n)
	cy /= float64(n)
	a, b, d := 0.0, 0.0, 0.0 // cov00, cov01, cov11
	for _, p := range pts {
		dx, dy := p[0]-cx, p[1]-cy
		a += dx * dx
		b += dx * dy
		d += dy * dy
	}
	a /= float64(n)
	b /= float64(n)
	d /= float64(n)
	half := (a - d) / 2
	lam1 := (a+d)/2 + math.Sqrt(half*half+b*b)
	lam2 := (a+d)/2 - math.Sqrt(half*half+b*b)
	if lam2 < 1e-9 {
		lam2 = 1e-9
	}
	evRatio = lam1 / lam2
	angle = 0.5 * math.Atan2(2*b, a-d)
	return
}
