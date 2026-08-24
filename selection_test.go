package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/usace-nsi/entwine-buildings/inventory"
)

// Local resources the test depends on, relative to the repo root
// (where this test lives and where `go test` runs).
const (
	cacheDir   = "inv-cache"
	parcelFile = "example/res-parcel4.geojson"
)

// -expected=NAME pins which resource this parcel must select.
// Leave empty to only assert "a match was found"; the -v log line
// tells you what it actually picked so you can pin it.
var expectedName string

func TestMain(m *testing.M) {
	flag.StringVar(&expectedName, "expected", "", "expected inventory resource name (empty = only assert a match)")
	flag.Parse()
	os.Exit(m.Run())
}

// TestSelectParcel4FromCache is the inventory path from main.go, minus
// the network: cached snapshot in ./inv-cache + local parcel file.
func TestSelectParcel4FromCache(t *testing.T) {
	if _, err := os.Stat(cacheDir); err != nil {
		t.Skipf("inventory cache %q not found; run the CLI once with -inventory-dir %s to create it", cacheDir, cacheDir)
	}
	if _, err := os.Stat(parcelFile); err != nil {
		t.Skipf("parcel file %q not found: %v", parcelFile, err)
	}

	inv, err := inventory.Open(cacheDir)
	if err != nil {
		t.Fatalf("opening cached inventory: %v", err)
	}

	rings := loadRings(t, parcelFile)
	requireDegreeSpace(t, rings)

	sel := inv.SelectRings(rings...)
	if !sel.Matched {
		t.Fatalf("no resource selected for %s", parcelFile)
	}
	if sel.Zone < 1 || sel.Zone > 60 {
		t.Errorf("implausible UTM zone %d", sel.Zone)
	}
	if sel.Resource.URL == "" {
		t.Error("matched resource has empty URL")
	}
	if expectedName != "" && sel.Resource.Name != expectedName {
		t.Errorf("selected %q, expected %q", sel.Resource.Name, expectedName)
	}
	fmt.Printf("selected %s (utm zone %d, %d pts)\n  %s",
		sel.Resource.Name, sel.Zone, sel.Resource.Count, sel.Resource.URL)

	// Selection must be deterministic (ties broken by name).
	if sel2 := inv.SelectRings(rings...); sel2.Resource.Name != sel.Resource.Name {
		t.Errorf("nondeterministic selection: %q then %q", sel.Resource.Name, sel2.Resource.Name)
	}
}

// TestRefreshFailsFallsBackToCache covers the refresh-failure branch of
// main.go: a refresh that can't reach the network must not block a run
// when a usable snapshot exists.
func TestRefreshFailsFallsBackToCache(t *testing.T) {
	if _, err := os.Stat(cacheDir); err != nil {
		t.Skipf("inventory cache %q not found", cacheDir)
	}

	client := inventory.Client{HTTP: &http.Client{Transport: roundTripError{}}} // adjust field name if Client's doesn't match
	_, _, err := inventory.Refresh(context.Background(), cacheDir, client)
	if err == nil {
		// Refresh didn't need the network (e.g. freshness TTL). Also a
		// valid branch — nothing to assert beyond Open working below.
	}

	cached, oerr := inventory.Open(cacheDir)
	if oerr != nil {
		t.Fatalf("cache open failed (%v) — refresh error was %v", oerr, err)
	}
	rings := loadRings(t, parcelFile)
	requireDegreeSpace(t, rings)
	if sel := cached.SelectRings(rings...); !sel.Matched {
		t.Fatalf("cached snapshot failed to select a resource for %s", parcelFile)
	}
}

type roundTripError struct{}

func (roundTripError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in test")
}

// loadRings reads a GeoJSON file (FeatureCollection, Feature, or bare
// geometry) and returns the OUTER ring of every polygon. Holes are
// skipped: they lie inside the outer ring and add no coverage signal.
func loadRings(t *testing.T, path string) []inventory.Ring {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc struct {
		Type     string `json:"type"`
		Geometry json.RawMessage
		Features []struct {
			Geometry json.RawMessage
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var geoms []json.RawMessage
	switch {
	case doc.Type == "FeatureCollection":
		for _, f := range doc.Features {
			geoms = append(geoms, f.Geometry)
		}
	case doc.Type == "Feature", doc.Type == "Polygon", doc.Type == "MultiPolygon":
		geoms = []json.RawMessage{doc.Geometry}
	default:
		t.Fatalf("%s: unrecognized GeoJSON type %q", path, doc.Type)
	}
	if len(geoms) == 0 {
		t.Fatalf("%s: no geometries found", path)
	}
	var rings []inventory.Ring
	for i, g := range geoms {
		var gm struct {
			Type        string `json:"type"`
			Coordinates json.RawMessage
		}
		if err := json.Unmarshal(g, &gm); err != nil {
			t.Fatalf("geometry %d in %s: %v", i, path, err)
		}
		switch gm.Type {
		case "Polygon":
			var rings2 [][][]float64 // ring -> position -> [lon, lat(, z)]
			if err := json.Unmarshal(gm.Coordinates, &rings2); err != nil {
				t.Fatalf("polygon coords in %s: %v", path, err)
			}
			if len(rings2) == 0 {
				t.Fatalf("polygon in %s has no rings", path)
			}
			rings = append(rings, toRing(rings2[0]))
		case "MultiPolygon":
			var polys [][][][]float64 // poly -> ring -> position -> [lon, lat]
			if err := json.Unmarshal(gm.Coordinates, &polys); err != nil {
				t.Fatalf("multipolygon coords in %s: %v", path, err)
			}
			for _, poly := range polys {
				if len(poly) == 0 {
					continue
				}
				rings = append(rings, toRing(poly[0]))
			}
		default:
			t.Fatalf("geometry %d in %s: unsupported type %q", i, path, gm.Type)
		}
	}
	if len(rings) == 0 {
		t.Fatalf("%s: no polygon rings found", path)
	}
	return rings
}

func toRing(pos [][]float64) inventory.Ring {
	out := make([][2]float64, len(pos))
	for i, p := range pos {
		if len(p) < 2 {
			panic(fmt.Sprintf("short position %v", p))
		}
		out[i] = [2]float64{p[0], p[1]}
	}
	return out
}

// requireDegreeSpace guards against the meter-space bug: the inventory
// grid is WGS84 degrees, and projected coordinates silently degrade
// selection into a full-grid sweep.
func requireDegreeSpace(t *testing.T, rings []inventory.Ring) {
	t.Helper()
	for i, r := range rings {
		for j, p := range r {
			if p[0] < -180 || p[0] > 180 || p[1] < -90 || p[1] > 90 {
				t.Fatalf("ring %d point %d (%v) is not WGS84 degrees — selection requires degree-space coordinates", i, j, p)
			}
		}
	}
}
