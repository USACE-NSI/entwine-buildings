// buildings/building.go  (Options gains ParcelAng; pickAngle gets its
// two new arguments; the points import is gone)
package buildings

import (
	"math"

	"github.com/usace-nsi/entwine-buildings/geometry"
	"github.com/usace-nsi/entwine-buildings/raster"
)

// Options are the tunables that shape a Building.
type Options struct {
	Grid         raster.Grid
	GridDTM      raster.Grid
	OrientRidge  bool
	SnapToParcel float64
	FlatRelief   float64
	ParcelAng    float64 // parcel's long-axis bearing (rad), for the snap fallback
}

// RoofInfo classifies a roof from the DSM-DTM cells inside a footprint.
type RoofInfo struct {
	Kind     string  // "flat", "pitched", or "unknown"
	SlopeDeg float64 // 0 for flat/unknown
	RidgeDeg float64 // ridge bearing (deg from north) when the top band is a line
	Relief   float64 // zmax - zmin over the roof cells
	cells    [][3]float64
}

// Height is the p90 of the roof cells' height-above-ground.
func (r RoofInfo) Height() float64 {
	if len(r.cells) == 0 {
		return 0
	}
	vals := make([]float64, len(r.cells))
	for i, c := range r.cells {
		vals[i] = c[2]
	}
	return geometry.Percentile(vals, 0.9)
}

// ClassifyRoof fills in Kind/SlopeDeg/RidgeDeg/Relief from the roof cells.
// cells are (x, y, heightAboveGround) in projected meters.
func (r *RoofInfo) ClassifyRoof(cells [][3]float64, b geometry.Box2, flatRelief float64) {
	r.cells = cells
	if len(cells) == 0 {
		r.Kind = "unknown"
		return
	}
	zmax, zmin := math.Inf(-1), math.Inf(1)
	for _, c := range cells {
		if c[2] > zmax {
			zmax = c[2]
		}
		if c[2] < zmin {
			zmin = c[2]
		}
	}
	relief := zmax - zmin
	r.Relief = relief
	if relief < flatRelief {
		r.Kind = "flat"
		return
	}
	// Top band: cells in the top 30% of the roof relief.
	cut := zmax - 0.3*relief
	var top [][2]float64
	for _, c := range cells {
		if c[2] >= cut {
			top = append(top, [2]float64{c[0], c[1]})
		}
	}
	var slopeDeg, ridgeDeg float64
	if len(top) >= 4 {
		ang, _, _, evR := geometry.PcaAngle(top)
		if evR > 3 {
			// Top band is a line: a ridge running the length (gable).
			ridgeDeg = ang * 180 / math.Pi
			perp := widthPerp(b, ang)
			if perp > 0.5 {
				slopeDeg = math.Atan(relief/(perp/2)) * 180 / math.Pi
			}
		} else {
			// Top band is a blob: peak (hip). Rise spans the short side.
			if b.Short > 0.5 {
				slopeDeg = math.Atan(relief/(b.Short/2)) * 180 / math.Pi
			}
		}
	} else if b.Short > 0.5 {
		slopeDeg = math.Atan(relief/(b.Short/2)) * 180 / math.Pi
	}
	if slopeDeg > 60 {
		slopeDeg = 60
	}
	r.Kind = "pitched"
	r.SlopeDeg = slopeDeg
	r.RidgeDeg = ridgeDeg
}

// widthPerp: the box dimension perpendicular to a ridge bearing. If the
// ridge runs along the box's long axis, the rise spans the short side,
// and vice versa.
func widthPerp(b geometry.Box2, ang float64) float64 {
	d := math.Mod(math.Mod(ang-b.Angle, math.Pi)+math.Pi, math.Pi)
	if d > math.Pi/2 {
		d = math.Pi - d
	}
	if d < math.Pi/4 {
		return b.Short
	}
	return b.Long
}

// Building is one oriented footprint with its roof, height, and votes.
type Building struct {
	G           Group
	Box         geometry.Box2
	Corners     [][2]float64 // lon/lat, closed
	Area        float64
	Roof        RoofInfo
	HullFrac    float64
	MeanInt     float64
	LastRetFrac float64
	BldFrac     float64
	VegFrac     float64
	IntKnown    bool
	RetKnown    bool
	ClsKnown    bool
	Votes       int
	Kept        bool
	Reason      string
	AngSrc      string
}

// Height is the building's height above ground: the p90 of the roof
// cells' height-above-ground, delegated to the roof classifier.
func (b Building) Height() float64 { return b.Roof.Height() }

// Buildings is a set of buildings.
type Buildings []Building

// NewFromGroups builds a Building per group: oriented box, roof/height
// from the DSM/DTM grid, and the point aggregates used for tree votes.
func NewFromGroups(groups []Group, opt Options) Buildings {
	blds := make(Buildings, 0, len(groups))
	for _, g := range groups {
		ang, angSrc := pickAngle(g.LL, g.Points, opt.OrientRidge, opt.SnapToParcel, opt.ParcelAng, true)
		bx := geometry.BoxAt(g.LL, ang)
		area := bx.Area()
		corners := make([][2]float64, 0, 5)
		for _, c := range bx.Corners {
			lon, lat := geometry.Wgs84From3857(c[0], c[1])
			corners = append(corners, [2]float64{lon, lat})
		}
		corners = append(corners, corners[0])

		// Height + roof from the DSM/DTM grid inside the box.
		cells := opt.Grid.HeightCells(opt.GridDTM, bx.Corners, 0.05)
		var roof RoofInfo
		roof.cells = cells
		roof.ClassifyRoof(cells, bx, opt.FlatRelief)

		// Point aggregates (votes).
		var b Building
		b.G, b.Box, b.Corners, b.Area, b.Roof, b.AngSrc = g, bx, corners, area, roof, angSrc
		hull := geometry.ConvexHull(g.LL)
		if area > 0 {
			b.HullFrac = geometry.HullArea(hull) / area
		}
		sumInt, nInt := 0.0, 0
		nRet, lastRet, nCls, nBld, nVeg := 0, 0, 0, 0, 0
		for _, p := range g.Points {
			if p.Intensity > 0 {
				sumInt += p.Intensity
				nInt++
			}
			if p.NumRet > 0 {
				nRet++
				if p.RetNum == p.NumRet {
					lastRet++
				}
			}
			if p.SrcClass > 0 {
				nCls++
				switch p.SrcClass {
				case 6: // building
					nBld++
				case 3, 4, 5: // low/medium/high vegetation
					nVeg++
				}
			}
		}
		b.IntKnown = nInt > 0
		b.RetKnown = nRet > 0
		b.ClsKnown = nCls > 0
		if nInt > 0 {
			b.MeanInt = sumInt / float64(nInt)
		}
		if nRet > 0 {
			b.LastRetFrac = float64(lastRet) / float64(nRet)
		}
		if nCls > 0 {
			b.BldFrac = float64(nBld) / float64(nCls)
			b.VegFrac = float64(nVeg) / float64(nCls)
		}
		blds = append(blds, b)
	}
	return blds
}
