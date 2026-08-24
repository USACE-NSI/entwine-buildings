// buildings/trees.go  (import block only changes)
package buildings

import (
	"github.com/usace-nsi/entwine-buildings/geometry"
)

// TreeVotes counts the 4 tree votes for a building: round hull, dark,
// penetrating, vegetation class. intCut is the auto dark-intensity cut
// (0 disables the dark vote).
func (b *Building) TreeVotes(intCut float64, hullRound, lastRetCut float64) int {
	v := 0
	if b.HullFrac > 0 && b.HullFrac < hullRound {
		v++ // round in plan
	}
	if b.IntKnown && intCut > 0 && b.MeanInt < intCut {
		v++ // dark
	}
	if b.RetKnown && b.LastRetFrac < lastRetCut {
		v++ // penetrating canopy
	}
	if b.ClsKnown && b.VegFrac > 0.5 && b.BldFrac < 0.1 {
		v++ // vegetation class votes
	}
	return v
}

// DarkIntensityCut is the percentile of the buildings' mean intensities
// used as the dark-intensity cut. Returns 0 when there are too few known
// intensities or pct is not in (0,100].
func (bs Buildings) DarkIntensityCut(pct float64) float64 {
	if pct <= 0 {
		return 0
	}
	var ints []float64
	for _, b := range bs {
		if b.IntKnown {
			ints = append(ints, b.MeanInt)
		}
	}
	if len(ints) < 4 {
		return 0
	}
	return geometry.Percentile(ints, pct/100)
}

// Filter applies the tree votes and marks each building kept/dropped.
// voteMin <= 0 disables filtering (everything kept). Returns the number
// of dropped buildings.
func (bs Buildings) Filter(voteMin int, hullRound, lastRetCut float64, intPct float64) int {
	if voteMin <= 0 {
		for i := range bs {
			bs[i].Kept = true
		}
		return 0
	}
	intCut := bs.DarkIntensityCut(intPct)
	dropped := 0
	for i := range bs {
		b := &bs[i]
		v := b.TreeVotes(intCut, hullRound, lastRetCut)
		b.Votes = v
		if v >= voteMin {
			b.Kept = false
			b.Reason = "tree"
			dropped++
		} else {
			b.Kept = true
		}
	}
	return dropped
}
