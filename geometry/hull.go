// geometry/hull.go
package geometry

import (
	"math"
	"sort"
)

// ConvexHull: Andrew's monotone chain. Returns the hull CCW, not repeated.
func ConvexHull(pts [][2]float64) [][2]float64 {
	n := len(pts)
	if n < 3 {
		return pts
	}
	p := append([][2]float64(nil), pts...)
	sort.Slice(p, func(i, j int) bool {
		if p[i][0] != p[j][0] {
			return p[i][0] < p[j][0]
		}
		return p[i][1] < p[j][1]
	})
	cross := func(o, a, b [2]float64) float64 {
		return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
	}
	hull := make([][2]float64, 0, n)
	for _, q := range p {
		for len(hull) >= 2 && cross(hull[len(hull)-2], hull[len(hull)-1], q) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, q)
	}
	upper := len(hull)
	for i := n - 1; i >= 0; i-- {
		q := p[i]
		for len(hull) >= upper+1 && cross(hull[len(hull)-2], hull[len(hull)-1], q) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, q)
	}
	if len(hull) > 1 {
		hull = hull[:len(hull)-1]
	}
	return hull
}

// HullArea is the area of a convex hull (shoelace).
func HullArea(h [][2]float64) float64 {
	if len(h) < 3 {
		return 0
	}
	a := 0.0
	for i := 0; i < len(h); i++ {
		p, q := h[i], h[(i+1)%len(h)]
		a += p[0]*q[1] - q[0]*p[1]
	}
	return math.Abs(a) / 2
}
