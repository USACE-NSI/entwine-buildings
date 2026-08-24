// buildings/angle.go
package buildings

import (
	"math"
	"sort"

	"github.com/usace-nsi/entwine-buildings/geometry"
	"github.com/usace-nsi/entwine-buildings/points"
)

// PickAngle picks the box bearing for a building: the roof-ridge bearing
// (top band) when the ridge signal is strong and useRidge is set, else
// full-cloud PCA with the near-square parcel snap. Second return is the
// source: "ridge", "pca", or "pca-snap".
func pickAngle(ll [][2]float64, pts []points.Point, useRidge bool, snap float64, parcelAng float64, parcelKnown bool) (float64, string) {
	if useRidge {
		if a, ok := ridgeBearing(pts); ok {
			return a, "ridge"
		}
	}
	ang, _, _, evR := geometry.PcaAngle(ll)
	if snap > 0 && parcelKnown && evR < snap {
		return snapToParcelAngle(ang, parcelAng), "pca-snap"
	}
	return ang, "pca"
}

// snapToParcelAngle: nearest of {p, p+90, p+180, p+270} to the building's
// own PCA angle. Used when the building is near-square and its own
// orientation is unreliable.
func snapToParcelAngle(a, p float64) float64 {
	best, bd := p, math.Inf(1)
	for k := 0; k < 4; k++ {
		c := p + float64(k)*math.Pi/2
		d := math.Mod(math.Mod(a-c, math.Pi)+math.Pi, math.Pi)
		if d > math.Pi/2 {
			d = math.Pi - d
		}
		if d < bd {
			bd, best = d, c
		}
	}
	return best
}

// RidgeBearing estimates the ridge/long-axis bearing (radians, modulo pi)
// from the cluster's TOP BAND only — the roof ridge for gable roofs. Tree
// tops are rare in the cloud, so the upper roof surface is the cleanest
// orientation signal we have. Returns ok=false when the signal is too weak
// (flat roof, too few points, blob-like hip apex) and the caller should
// fall back to full-cloud PCA.
func ridgeBearing(pts []points.Point) (float64, bool) {
	if len(pts) < 6 {
		return 0, false
	}
	m := make([][2]float64, len(pts))
	zs := make([]float64, len(pts))
	for i := range pts {
		x, y := geometry.Wgs84To3857(pts[i].Lon, pts[i].Lat)
		m[i] = [2]float64{x, y}
		zs[i] = pts[i].Z
	}
	sorted := append([]float64(nil), zs...)
	sort.Float64s(sorted)
	zmin := sorted[0]
	// p90, not zmax: a few fused tree tops would otherwise corrupt the cut.
	zRidge := geometry.PctlFloats(sorted, 0.90)
	relief := zRidge - zmin
	if relief < 1.0 { // flat roof: no ridge to orient on
		return 0, false
	}
	cut := zRidge - 0.30*relief // top 30% of the roof surface
	var band [][2]float64
	for i := range pts {
		if pts[i].Z >= cut {
			band = append(band, m[i])
		}
	}
	if len(band) < 5 {
		return 0, false
	}
	a, cx, cy, _ := geometry.PcaAngle(band)
	// Compactness guard: a hip apex or blob band has an unreliable PCA
	// bearing. Measure the band's spread along its own principal axes.
	cosA, sinA := math.Cos(a), math.Sin(a)
	minU, maxU := math.Inf(1), math.Inf(-1)
	minV, maxV := math.Inf(1), math.Inf(-1)
	for _, p := range band {
		dx, dy := p[0]-cx, p[1]-cy
		u := dx*cosA + dy*sinA
		v := -dx*sinA + dy*cosA
		if u < minU {
			minU = u
		}
		if u > maxU {
			maxU = u
		}
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	aspect := (maxU - minU) / math.Max(1e-6, maxV-minV)
	if aspect < 1.2 { // blob, not a line: don't trust it
		return 0, false
	}
	return a, true
}
