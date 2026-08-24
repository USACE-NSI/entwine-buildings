// geometry/crs.go
// Package geometry holds the pure coordinate math: WGS84 <-> EPSG:3857
// conversion, PCA, oriented boxes, hulls, and percentile helpers.
package geometry

import "math"

// MercR is the EPSG:3857 spherical radius.
const MercR = 6378137.0

// Wgs84To3857 converts WGS84 lon/lat to EPSG:3857 x/y (meters) using the
// closed-form spherical Mercator. Latitude is clamped to the Mercator limit.
func Wgs84To3857(lon, lat float64) (x, y float64) {
	const lim = 85.05112877980659
	if lat > lim {
		lat = lim
	} else if lat < -lim {
		lat = -lim
	}
	lr := lon * math.Pi / 180.0
	latr := lat * math.Pi / 180.0
	return MercR * lr, MercR * math.Log(math.Tan(math.Pi/4+latr/2))
}

// Wgs84From3857 inverts Wgs84To3857 (same closed-form spherical Mercator).
func Wgs84From3857(x, y float64) (lon, lat float64) {
	lon = x / MercR * 180.0 / math.Pi
	lat = (2*math.Atan(math.Exp(y/MercR)) - math.Pi/2) * 180.0 / math.Pi
	return
}