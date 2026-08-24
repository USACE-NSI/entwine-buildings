// geometry/box2.go
package geometry

import "math"

// Box2 is a rectangle aligned to a given bearing, in projected (meter) coords.
type Box2 struct {
	Corners [4][2]float64 // (minx,miny),(maxx,miny),(maxx,maxy),(minx,maxy) in the rotated frame
	Angle   float64       // long-axis bearing (rad)
	Long    float64
	Short   float64
}

// Area of the box.
func (b Box2) Area() float64 { return b.Long * b.Short }

// BoxAt returns the oriented bounding box of pts around the given bearing.
func BoxAt(pts [][2]float64, ang float64) Box2 {
	cx, cy := 0.0, 0.0
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	n := float64(len(pts))
	cx /= n
	cy /= n
	ca, sa := math.Cos(ang), math.Sin(ang)
	minx, miny, maxx, maxy := math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64
	for _, p := range pts {
		dx, dy := p[0]-cx, p[1]-cy
		rx := dx*ca + dy*sa // rotate into the rotated frame
		ry := -dx*sa + dy*ca
		if rx < minx {
			minx = rx
		}
		if rx > maxx {
			maxx = rx
		}
		if ry < miny {
			miny = ry
		}
		if ry > maxy {
			maxy = ry
		}
	}
	lo := func(x, y float64) [2]float64 {
		return [2]float64{cx + x*ca - y*sa, cy + x*sa + y*ca}
	}
	return Box2{
		Corners: [4][2]float64{
			lo(minx, miny), lo(maxx, miny), lo(maxx, maxy), lo(minx, maxy),
		},
		Angle: ang,
		Long:  maxx - minx,
		Short: maxy - miny,
	}
}
