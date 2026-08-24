// buildings/output.go  (Coordinates type fixed; imports fixed)
package buildings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/usace-nsi/entwine-buildings/geometry"
)

type gjGeometry struct {
	Type        string         `json:"type"`
	Coordinates [][][2]float64 `json:"coordinates"` // ring = [][2]float64
}

type gjFeature struct {
	Type       string                 `json:"type"`
	Geometry   gjGeometry             `json:"geometry"`
	Properties map[string]interface{} `json:"properties"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// WriteDebugCSV writes components.csv with per-building features for
// threshold tuning.
func (bs Buildings) WriteDebugCSV(path string) error {
	var b strings.Builder
	b.WriteString("id,area_m2,height_m,relief,roof,slope_deg,ridge_deg,orient,orient_deg,hull_frac,lastret_frac,mean_intensity,bld_frac,veg_frac,tree_votes,kept,reason\n")
	for i, bl := range bs {
		fmt.Fprintf(&b, "B%d,%.1f,%.2f,%.2f,%s,%.1f,%.1f,%s,%.1f,%.3f,%.3f,%.0f,%.3f,%.3f,%d,%t,%s\n",
			i+1, bl.Area, bl.Height(), bl.Roof.Relief, bl.Roof.Kind, bl.Roof.SlopeDeg, bl.Roof.RidgeDeg,
			bl.AngSrc, geometry.NormalizeOrientDeg(bl.Box.Angle),
			bl.HullFrac, bl.LastRetFrac, bl.MeanInt, bl.BldFrac, bl.VegFrac, bl.Votes, bl.Kept, bl.Reason)
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

// WriteGeoJSON writes the kept buildings (sorted by area, largest first)
// as a GeoJSON FeatureCollection and returns the feature count.
func (bs Buildings) WriteGeoJSON(path string) (int, error) {
	kept := make(Buildings, 0, len(bs))
	for _, b := range bs {
		if b.Kept {
			kept = append(kept, b)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Area > kept[j].Area })

	fc := struct {
		Type     string      `json:"type"`
		Features []gjFeature `json:"features"`
	}{Type: "FeatureCollection"}
	for i, b := range kept {
		fc.Features = append(fc.Features, gjFeature{
			Type: "Feature",
			Geometry: gjGeometry{
				Type:        "Polygon",
				Coordinates: [][][2]float64{b.Corners},
			},
			Properties: map[string]interface{}{
				"id":             fmt.Sprintf("B%d", i+1),
				"roof":           b.Roof.Kind,
				"slope_deg":      round1(b.Roof.SlopeDeg),
				"ridge_deg":      round1(b.Roof.RidgeDeg),
				"height_m":       round2(b.Height()),
				"area_m2":        round1(b.Area),
				"area_sqft":      round1(b.Area / 0.09290304),
				"points":         len(b.G.Points),
				"tree_votes":     b.Votes,
				"potential_tree": !b.Kept,
				"method":         "ept-dsm-dtm-box",
			},
		})
	}
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshaling GeoJSON: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return 0, err
	}
	return len(fc.Features), nil
}
