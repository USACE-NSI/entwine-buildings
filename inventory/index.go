package inventory

import (
	"math"
	"sort"
)

// Ring is a GeoJSON ring in WGS84 (lon, lat) — the same shape as the
// runner's parcel rings, so they can be passed straight in.
type Ring = [][2]float64

// Selection is the outcome of matching a location against the inventory.
// A zero Selection (Matched == false) means no EPT resource covers the
// location: route the parcel to the census-only fallback.
type Selection struct {
	Resource Resource // the chosen resource; zero when Matched == false
	Zone     int      // UTM zone for the query location
	Matched  bool
}

// UTMZone returns the UTM zone number for a WGS84 longitude using the
// standard 6-degree assignment (1..60). No CONUS special-case zones
// are applied; for mainland US parcels this matches the runner's own
// zone derivation from parcel longitude.
func UTMZone(lon float64) int {
	z := int(math.Floor((lon+180)/6)) + 1
	if z < 1 {
		z = 1
	}
	if z > 60 {
		z = 60
	}
	return z
}

// rank orders resources: FullState first, newest acquisition year,
// highest point count.
type rank struct {
	fullState bool
	year      int
	count     int64
}

func (a rank) less(b rank) bool {
	if a.fullState != b.fullState {
		return a.fullState
	}
	if a.year != b.year {
		return a.year > b.year
	}
	return a.count > b.count
}

func rankOf(r *Resource) rank {
	return rank{fullState: IsFullState(r.Name), year: r.Year, count: r.Count}
}

// Select returns the best-matching resource for a point, or a zero
// Selection when no resource covers it.
func (inv *Inventory) Select(lon, lat float64) Selection {
	var best *Resource
	for _, i := range inv.grid.pointCandidates(lon, lat) {
		r := &inv.resources[i]
		if !r.contains(lon, lat) {
			continue
		}
		if best == nil || rankOf(r).less(rankOf(best)) {
			best = r
		}
	}
	if best == nil {
		return Selection{}
	}
	return Selection{Resource: *best, Zone: UTMZone(lon), Matched: true}
}

// Candidates returns every resource containing the point, sorted
// best-first.
func (inv *Inventory) Candidates(lon, lat float64) []Selection {
	var out []Selection
	for _, i := range inv.grid.pointCandidates(lon, lat) {
		r := &inv.resources[i]
		if !r.contains(lon, lat) {
			continue
		}
		out = append(out, Selection{Resource: *r, Zone: UTMZone(lon), Matched: true})
	}
	sort.Slice(out, func(a, b int) bool {
		ra, rb := rankOf(&out[a].Resource), rankOf(&out[b].Resource)
		if ra.less(rb) {
			return true
		}
		if rb.less(ra) {
			return false
		}
		return out[a].Resource.Name < out[b].Resource.Name
	})
	return out
}

// SelectRings selects the resource for a parcel polygon. Every parcel
// vertex plus the vertex-mean centroid is tested against each grid-culled
// candidate; the candidate covering the most of those points wins, with
// ties broken by rank (FullState, then year, then count) and then name
// for determinism. Hole rings are treated as extra vertex sources,
// which is a harmless over-approximation for coverage.
func (inv *Inventory) SelectRings(rings ...Ring) Selection {
	if len(rings) == 0 {
		return Selection{}
	}
	var box Box
	var pts [][2]float64
	for _, ring := range rings {
		for i, p := range ring {
			if i == 0 && len(pts) == 0 {
				box = Box{p[0], p[1], p[0], p[1]}
			} else {
				box = box.Extend(p[0], p[1])
			}
			pts = append(pts, p)
		}
	}
	if len(pts) == 0 {
		return Selection{}
	}
	cLon, cLat := 0.0, 0.0
	for _, p := range pts {
		cLon += p[0]
		cLat += p[1]
	}
	cLon /= float64(len(pts))
	cLat /= float64(len(pts))
	pts = append(pts, [2]float64{cLon, cLat})

	type hit struct {
		idx   int
		pts   int
		r     rank
		name  string
	}
	var best *hit
	for _, i := range inv.grid.boxCandidates(box) {
		r := &inv.resources[i]
		n := 0
		for _, p := range pts {
			if r.contains(p[0], p[1]) {
				n++
			}
		}
		if n == 0 {
			continue
		}
		h := hit{idx: i, pts: n, r: rankOf(r), name: r.Name}
		if best == nil || h.better(*best) {
			best = &h
		}
	}
	if best == nil {
		return Selection{}
	}
	r := &inv.resources[best.idx]
	return Selection{Resource: *r, Zone: UTMZone(cLon), Matched: true}
}

func (a hit) better(b hit) bool {
	if a.pts != b.pts {
		return a.pts > b.pts
	}
	if a.r.less(b.r) {
		return true
	}
	if b.r.less(a.r) {
		return false
	}
	return a.name < b.name
}

// contains reports whether the point falls inside the resource coverage
// (even-odd over all rings, holes included), gated on the resource box.
func (r *Resource) contains(lon, lat float64) bool {
	if !r.Box.Contains(lon, lat) {
		return false
	}
	return pointInRings(lon, lat, r.rings)
}

// pointInRings is a vertical ray-cast over all rings; with holes the
// even-odd rule across outer+inner rings gives correct containment.
func pointInRings(lon, lat float64, rings [][][2]float64) bool {
	inside := false
	for _, ring := range rings {
		n := len(ring)
		for i, j := 0, n-1; i < n; j = i {
			pi, pj := ring[i], ring[j]
			if (pi[1] > lat) != (pj[1] > lat) {
				x := pi[0] + (lat-pi[1])/(pj[1]-pi[1]) * (pj[0]-pi[0])
				if lon < x {
					inside = !inside
				}
			}
		}
	}
	return inside
}

// grid is a uniform world grid (5-degree cells) keyed on resource
// bboxes. It is a culling index only: candidates it returns still go
// through exact point-in-polygon tests. A resource is inserted into
// every cell its bbox overlaps, so oversized resources (state-wide,
// wide-bbox projects) are handled without special cases.
type grid struct {
	cell  float64
	cells map[[2]int][]int // [col,row] -> resource indices
}

const gridCell = 5.0 // degrees

func newGrid(rs []Resource) *grid {
	g := &grid{cell: gridCell, cells: make(map[[2]int][]int)}
	for i, r := range rs {
		c0, c1 := cellIndex(r.Box.MinLon, gridCell, 360), cellIndex(r.Box.MaxLon, gridCell, 360)
		r0, r1 := cellIndex(r.Box.MinLat, gridCell, 180), cellIndex(r.Box.MaxLat, gridCell, 180)
		for c := c0; c <= c1; c++ {
			for row := r0; row <= r1; row++ {
				g.cells[[2]int{c, row}] = append(g.cells[[2]int{c, row}], i)
			}
		}
	}
	return g
}

// cellIndex maps a coordinate in [-extent/2, extent/2] to a grid cell
// of the given size, clamped to the valid range.
func cellIndex(v, cell, extent float64) int {
	i := int(math.Floor((v + extent/2) / cell))
	if i < 0 {
		i = 0
	}
	if i >= int(extent/cell) {
		i = int(extent/cell) - 1
	}
	return i
}

func (g *grid) pointCandidates(lon, lat float64) []int {
	return g.cells[[2]int{cellIndex(lon, g.cell, 360), cellIndex(lat, g.cell, 180)}]
}

func (g *grid) boxCandidates(b Box) []int {
	seen := map[int]bool{}
	var out []int
	c0, c1 := cellIndex(b.MinLon, g.cell, 360), cellIndex(b.MaxLon, g.cell, 360)
	r0, r1 := cellIndex(b.MinLat, g.cell, 180), cellIndex(b.MaxLat, g.cell, 180)
	for c := c0; c <= c1; c++ {
		for row := r0; row <= r1; row++ {
			for _, i := range g.cells[[2]int{c, row}] {
				if !seen[i] {
					seen[i] = true
					out = append(out, i)
				}
			}
		}
	}
	return out
}
