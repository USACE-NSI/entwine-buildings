// geometry/bbox.go
package geometry

import "math"

// PointInQuad: ray cast against a 4-corner box.
func PointInQuad(x, y float64, q [4][2]float64) bool {
	inside := false
	for i := 0; i < 4; i++ {
		a, b := q[i], q[(i+1)%4]
		if (a[1] > y) != (b[1] > y) {
			xint := a[0] + (y-a[1])*(b[0]-a[0])/(b[1]-a[1])
			if x < xint {
				inside = !inside
			}
		}
	}
	return inside
}

// OverlapFrac: fraction of the smaller box covered by the intersection,
// approximated with axis-aligned bounding boxes.
func (a Box2) OverlapFrac(b Box2) float64 {
	abb := func(b Box2) (minx, miny, maxx, maxy float64) {
		minx, miny, maxx, maxy = b.Corners[0][0], b.Corners[0][1], b.Corners[0][0], b.Corners[0][1]
		for _, c := range b.Corners {
			if c[0] < minx {
				minx = c[0]
			}
			if c[0] > maxx {
				maxx = c[0]
			}
			if c[1] < miny {
				miny = c[1]
			}
			if c[1] > maxy {
				maxy = c[1]
			}
		}
		return
	}
	ax0, ay0, ax1, ay1 := abb(a)
	bx0, by0, bx1, by1 := abb(b)
	ix0, iy0 := math.Max(ax0, bx0), math.Max(ay0, by0)
	ix1, iy1 := math.Min(ax1, bx1), math.Min(ay1, by1)
	if ix1 <= ix0 || iy1 <= iy0 {
		return 0
	}
	inter := (ix1 - ix0) * (iy1 - iy0)
	smaller := math.Min(a.Area(), b.Area())
	if smaller <= 0 {
		return 0
	}
	return inter / smaller
}
