// geometry/stats.go
package geometry

import (
	"math"
	"sort"
)

// Percentile of a (copied and sorted) float slice, p in [0,1].
func Percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	idx := p * float64(len(s)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	f := idx - float64(lo)
	return s[lo]*(1-f) + s[hi]*f
}

// PctlFloats: quantile of an ALREADY-SORTED float slice (0..1).
func PctlFloats(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	r := q * float64(len(sorted)-1)
	lo := int(r)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	f := r - float64(lo)
	return sorted[lo]*(1-f) + sorted[hi]*f
}
