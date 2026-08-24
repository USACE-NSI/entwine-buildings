// parcel/parcel.go
// Package parcel loads WGS84 parcel GeoJSON into rings and derives the
// bounds and WKT used by the PDAL EPT reader.
package parcel

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/usace-nsi/entwine-buildings/geometry"
)

// Ring is a sequence of WGS84 lon/lat pairs.
type Ring [][2]float64

type gjGeom struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// LoadRings parses a parcel GeoJSON (FeatureCollection, Feature, or bare
// geometry) into outer rings of its Polygon/MultiPolygon features.
func LoadRings(path string) ([]Ring, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var geoms []gjGeom
	// 1) FeatureCollection -> all feature geometries
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Geometry gjGeom `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &fc); err == nil && fc.Type == "FeatureCollection" {
		for _, f := range fc.Features {
			geoms = append(geoms, f.Geometry)
		}
	} else {
		// 2) single Feature
		var ft struct {
			Type     string `json:"type"`
			Geometry gjGeom `json:"geometry"`
		}
		if err := json.Unmarshal(data, &ft); err == nil && ft.Type == "Feature" {
			geoms = append(geoms, ft.Geometry)
		} else {
			// 3) bare geometry
			var g gjGeom
			if err := json.Unmarshal(data, &g); err != nil {
				return nil, fmt.Errorf("cannot parse GeoJSON: %w", err)
			}
			geoms = append(geoms, g)
		}
	}
	var ringsOut []Ring
	for _, g := range geoms {
		switch g.Type {
		case "Polygon":
			var rs [][][2]float64
			if err := json.Unmarshal(g.Coordinates, &rs); err != nil {
				return nil, err
			}
			if len(rs) == 0 {
				return nil, fmt.Errorf("empty polygon")
			}
			ringsOut = append(ringsOut, Ring(rs[0]))
		case "MultiPolygon":
			var mps [][][][2]float64
			if err := json.Unmarshal(g.Coordinates, &mps); err != nil {
				return nil, err
			}
			for _, poly := range mps {
				if len(poly) > 0 {
					ringsOut = append(ringsOut, Ring(poly[0]))
				}
			}
		default:
			return nil, fmt.Errorf("unsupported geometry type %q (want Polygon/MultiPolygon)", g.Type)
		}
	}
	if len(ringsOut) == 0 {
		return nil, fmt.Errorf("no rings found in %s", path)
	}
	return ringsOut, nil
}

// Rings is a set of parcel rings.
type Rings []Ring

// BboxArea is the area (deg^2) of the ring's axis-aligned bounding box.
func (r Ring) BboxArea() float64 {
	minLon, minLat, maxLon, maxLat := r[0][0], r[0][1], r[0][0], r[0][1]
	for _, p := range r[1:] {
		if p[0] < minLon {
			minLon = p[0]
		}
		if p[0] > maxLon {
			maxLon = p[0]
		}
		if p[1] < minLat {
			minLat = p[1]
		}
		if p[1] > maxLat {
			maxLat = p[1]
		}
	}
	return (maxLon - minLon) * (maxLat - minLat)
}

// WKT renders the ring as a WKT POLYGON in EPSG:3857 (for readers.ept),
// closing the ring if it is not already closed.
func (r Ring) WKT() string {
	rs := append([][2]float64(nil), r...)
	if len(rs) > 1 {
		f, l := rs[0], rs[len(rs)-1]
		if f[0] != l[0] || f[1] != l[1] {
			rs = append(rs, f)
		}
	}
	var b strings.Builder
	b.WriteString("POLYGON ((")
	for i, p := range rs {
		if i > 0 {
			b.WriteString(", ")
		}
		x, y := geometry.Wgs84To3857(p[0], p[1])
		fmt.Fprintf(&b, "%.4f %.4f", x, y)
	}
	b.WriteString("))")
	return b.String()
}

// Bounds returns the minLon, minLat, maxLon, maxLat of all rings.
func (rs Rings) Bounds() (minLon, minLat, maxLon, maxLat float64) {
	for _, r := range rs {
		for i, p := range r {
			if i == 0 {
				minLon, minLat, maxLon, maxLat = p[0], p[1], p[0], p[1]
				continue
			}
			if p[0] < minLon {
				minLon = p[0]
			}
			if p[0] > maxLon {
				maxLon = p[0]
			}
			if p[1] < minLat {
				minLat = p[1]
			}
			if p[1] > maxLat {
				maxLat = p[1]
			}
		}
	}
	return
}
