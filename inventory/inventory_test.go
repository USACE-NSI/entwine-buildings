package inventory

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fc1 = `{
  "type": "FeatureCollection",
  "name": "resources",
  "features": [
    {
      "type": "Feature",
      "properties": {"name": "IA_FullState", "id": 1, "count": 100, "url": "https://example.com/IA_FullState/ept.json"},
      "geometry": {"type": "Polygon", "coordinates": [[[-95, 40], [-90, 40], [-90, 43], [-95, 43], [-95, 40]]]}
    },
    {
      "type": "Feature",
      "properties": {"name": "IA_Dubuque_2015", "id": 2, "count": 5000, "url": "https://example.com/IA_Dubuque_2015/ept.json"},
      "geometry": {"type": "Polygon", "coordinates": [[[-90.5, 42.5], [-90, 42.5], [-90, 43], [-90.5, 43], [-90.5, 42.5]]]}
    }
  ]
}`

const fc2 = `{
  "type": "FeatureCollection",
  "name": "resources",
  "features": [
    {
      "type": "Feature",
      "properties": {"name": "IA_FullState", "id": 1, "count": 100, "url": "https://example.com/IA_FullState/ept.json"},
      "geometry": {"type": "Polygon", "coordinates": [[[-95, 40], [-90, 40], [-90, 43], [-95, 43], [-95, 40]]]}
    },
    {
      "type": "Feature",
      "properties": {"name": "IA_CerroGordo_2016", "id": 3, "count": 900, "url": "https://example.com/IA_CerroGordo_2016/ept.json"},
      "geometry": {"type": "Polygon", "coordinates": [[[-91.2, 40.5], [-90.8, 40.5], [-90.8, 41], [-91.2, 41], [-91.2, 40.5]]]}
    }
  ]
}`

func mustInv(t *testing.T, body string) *Inventory {
	t.Helper()
	inv, err := NewFromReader(bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewFromReader: %v", err)
	}
	return inv
}

func TestNewFromBytes(t *testing.T) {
	inv := mustInv(t, fc1)
	if inv.Count() != 2 {
		t.Fatalf("Count = %d, want 2", inv.Count())
	}
	byName := map[string]Resource{}
	for _, r := range inv.Resources() {
		byName[r.Name] = r
	}
	if byName["IA_Dubuque_2015"].Year != 2015 {
		t.Errorf("Dubuque year = %d, want 2015", byName["IA_Dubuque_2015"].Year)
	}
	if byName["IA_FullState"].Year != 0 {
		t.Errorf("FullState year = %d, want 0 (unknown)", byName["IA_FullState"].Year)
	}
}

func TestSelectPrefersFullState(t *testing.T) {
	inv := mustInv(t, fc1)
	// (-90.2, 42.7) is inside BOTH resources; FullState must win even
	// though the county dataset is newer and denser.
	sel := inv.Select(-90.2, 42.7)
	if !sel.Matched || sel.Resource.Name != "IA_FullState" {
		t.Fatalf("Select = %+v, want IA_FullState", sel)
	}
	if sel.Zone != 15 {
		t.Errorf("Zone = %d, want 15", sel.Zone)
	}
}

func TestSelectCountyOnly(t *testing.T) {
	inv := mustInv(t, fc1)
	sel := inv.Select(-94.0, 41.0) // FullState only
	if !sel.Matched || sel.Resource.Name != "IA_FullState" {
		t.Fatalf("Select = %+v, want IA_FullState", sel)
	}
	// Outside everything.
	sel = inv.Select(-89.0, 42.7)
	if sel.Matched {
		t.Fatalf("Select = %+v, want no match", sel)
	}
}

func TestSelectRings(t *testing.T) {
	inv := mustInv(t, fc1)
	// Parcel inside both -> FullState (coverage tie, rank wins).
	both := []Ring{{{-90.3, 42.6}, {-90.2, 42.6}, {-90.2, 42.7}, {-90.3, 42.7}, {-90.3, 42.6}}}
	sel := inv.SelectRings(both...)
	if !sel.Matched || sel.Resource.Name != "IA_FullState" {
		t.Fatalf("SelectRings(both) = %+v, want IA_FullState", sel)
	}
	// Parcel inside FullState only.
	fa := []Ring{{{-94.0, 41.0}, {-93.0, 41.0}, {-93.0, 42.0}, {-94.0, 41.0}}}
	sel = inv.SelectRings(fa...)
	if !sel.Matched || sel.Resource.Name != "IA_FullState" {
		t.Fatalf("SelectRings(full) = %+v, want IA_FullState", sel)
	}
	// Parcel outside everything.
	none := []Ring{{{-89.5, 42.6}, {-89.4, 42.6}, {-89.4, 42.7}, {-89.5, 42.6}}}
	sel = inv.SelectRings(none...)
	if sel.Matched {
		t.Fatalf("SelectRings(none) = %+v, want no match", sel)
	}
}

func TestHoleExcluded(t *testing.T) {
	const body = `{
	  "type": "FeatureCollection",
	  "features": [
	    {
	      "type": "Feature",
	      "properties": {"name": "LAKE", "id": 9, "count": 10, "url": "https://example.com/LAKE/ept.json"},
	      "geometry": {"type": "Polygon", "coordinates": [
	        [[0, 0], [2, 0], [2, 2], [0, 2], [0, 0]],
	        [[0.75, 0.75], [1.25, 0.75], [1.25, 1.25], [0.75, 1.25], [0.75, 0.75]]
	      ]}
	    }
	  ]
	}`
	inv := mustInv(t, body)
	if sel := inv.Select(1.5, 1.5); !sel.Matched {
		t.Error("point outside the hole should match")
	}
	if sel := inv.Select(1.0, 1.0); sel.Matched {
		t.Error("point inside the hole should not match")
	}
}

func TestParseYear(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"IA_FullState", 0},
		{"AL_17Co_1_2020", 2020},
		{"USGS_LPC_CA_Central_Valley_2017_LAS_2019", 2019},
		{"AL_19Co_1_B24", 2024},
		{"AK_NativeAKVillages_1_D22", 2022},
		{"ARRA-CA_CentralCoast-Z3_2010", 2010},
		{"IA_Dubuque_2015", 2015},
	}
	for _, c := range cases {
		if got := parseYear(c.name); got != c.want {
			t.Errorf("parseYear(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestUTMZone(t *testing.T) {
	cases := []struct {
		lon  float64
		want int
	}{
		{-93.6, 15}, // Iowa
		{-179.5, 1},
		{179.5, 60},
		{0, 31},
	}
	for _, c := range cases {
		if got := UTMZone(c.lon); got != c.want {
			t.Errorf("UTMZone(%v) = %d, want %d", c.lon, got, c.want)
		}
	}
}

func TestRefreshLifecycle(t *testing.T) {
	var conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch inm := r.Header.Get("If-None-Match"); inm {
		case "":
			w.Header().Set("Etag", `"v1"`)
			w.Header().Set("Last-Modified", "Mon, 01 Jan 2026 00:00:00 GMT")
			fmt.Fprint(w, fc1)
		case `"v1"`:
			conditional++
			if conditional == 1 {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			// Catalog changed upstream.
			w.Header().Set("Etag", `"v2"`)
			w.Header().Set("Last-Modified", "Tue, 02 Jan 2026 00:00:00 GMT")
			fmt.Fprint(w, fc2)
		default:
			t.Errorf("unexpected If-None-Match %q", inm)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := Client{SourceURL: srv.URL}
	ctx := context.Background()

	// 1) First download.
	inv, res, err := Refresh(ctx, dir, client)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if !res.Changed || len(res.Added) != 2 {
		t.Errorf("first refresh: Changed=%v Added=%v, want true + 2 names", res.Changed, res.Added)
	}
	if inv.Count() != 2 || inv.Meta().SHA256 == "" {
		t.Fatalf("first refresh: Count=%d SHA256=%q", inv.Count(), inv.Meta().SHA256)
	}

	// 2) 304 -> cache reused, unchanged.
	inv, res, err = Refresh(ctx, dir, client)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if res.Changed || res.Added != nil || res.Removed != nil {
		t.Errorf("304 refresh: %+v, want unchanged", res)
	}
	if inv.Count() != 2 {
		t.Errorf("304 refresh: Count = %d, want 2", inv.Count())
	}

	// 3) Upstream change -> 200, cache replaced, diff reported.
	inv, res, err = Refresh(ctx, dir, client)
	if err != nil {
		t.Fatalf("third Refresh: %v", err)
	}
	if !res.Changed {
		t.Fatalf("changed refresh: %+v, want Changed", res)
	}
	if len(res.Added) != 1 || res.Added[0] != "IA_CerroGordo_2016" || len(res.Removed) != 1 || res.Removed[0] != "IA_Dubuque_2015" {
		t.Errorf("diff = Added %v Removed %v", res.Added, res.Removed)
	}
	if sel := inv.Select(-91.0, 40.7); !sel.Matched || sel.Resource.Name != "IA_CerroGordo_2016" {
		t.Errorf("Select after update = %+v", sel)
	}

	// 4) Open works on the refreshed cache without network.
	inv2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if inv2.Count() != 2 || inv2.Meta().SHA256 != inv.Meta().SHA256 {
		t.Errorf("Open: Count=%d SHA256=%q", inv2.Count(), inv2.Meta().SHA256)
	}
}
