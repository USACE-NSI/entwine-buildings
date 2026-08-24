// Package inventory ingests the USGS 3DEP Entwine Point Tile (EPT) resource
// inventory published at usgs.entwine.io and selects, for a parcel polygon,
// the EPT resource that covers it.
//
// Ingest source
//
// A single FeatureCollection (DefaultSource) with one feature per EPT
// resource:
//
//	{ "name": "AK_BrooksCamp_2012", "id": 0, "count": 529285317,
//	  "url": "https://s3-us-west-2.amazonaws.com/usgs-lidar-public/AK_BrooksCamp_2012/ept.json" }
//
// plus a WGS84 lon/lat coverage polygon.
//
// Caching
//
// The collection is several MB, so it is cached on disk with an ETag /
// Last-Modified sidecar (meta.json). Refresh issues a conditional GET:
// 304 -> the cache is reused (no data transfer); 200 -> the file is
// atomically replaced and the index rebuilt. The inventory is versioned
// by the SHA-256 of the file, so results stay reproducible until an
// explicit refresh; refresh reports which resource names were added or
// removed so a data update is a visible event, not a silent behavior
// change.
//
// Selection
//
// A uniform world grid over the resource bboxes culls candidates; a
// point-in-polygon test (even-odd over all rings, holes included)
// confirms coverage. Matches rank *_FullState resources first, then
// newest acquisition year (parsed from the resource name), then point
// count. A parcel with no match is a first-class outcome
// (Selection.Matched == false) and routes to the census-only fallback.
//
// An Inventory is immutable after construction; all read methods are
// safe for concurrent use.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultSource is the published EPT resource inventory (one
// FeatureCollection covering every USGS 3DEP EPT dataset).
const DefaultSource = "https://usgs.entwine.io/boundaries/resources.geojson"

const (
	dataFile = "resources.geojson"
	metaFile = "meta.json"
)

// Meta is the sidecar written next to the cached resources.geojson.
type Meta struct {
	ETag         string    `json:"etag"`
	LastModified string    `json:"lastModified"`
	SHA256       string    `json:"sha256"`
	Source       string    `json:"source"`
	FetchedAt    time.Time `json:"fetchedAt"`
	Count        int       `json:"count"`
}

// Client configures how Refresh talks to the source.
type Client struct {
	HTTP *http.Client // nil -> &http.Client{Timeout: 5*time.Minute}
	// SourceURL overrides DefaultSource.
	SourceURL string
}

// RefreshResult reports what a Refresh did.
type RefreshResult struct {
	Changed bool
	Added   []string // resource names new since the previous cache
	Removed []string // resource names dropped since the previous cache
	Meta    Meta
}

// Resource is one EPT dataset in the USGS archive.
type Resource struct {
	Name  string
	ID    int
	Count int64 // total point count in the dataset
	URL   string // https://.../<name>/ept.json
	Year  int   // acquisition year parsed from Name; 0 if unknown
	Box   Box   // lon/lat bounds of the coverage geometry

	rings [][][2]float64 // all rings (outers + holes), WGS84 lon/lat
}

// Box is an axis-aligned lon/lat box.
type Box struct {
	MinLon, MinLat, MaxLon, MaxLat float64
}

// Contains reports whether the point is inside the box.
func (b Box) Contains(lon, lat float64) bool {
	return lon >= b.MinLon && lon <= b.MaxLon && lat >= b.MinLat && lat <= b.MaxLat
}

// Extend returns the box grown to include the point.
func (b Box) Extend(lon, lat float64) Box {
	if lon < b.MinLon {
		b.MinLon = lon
	}
	if lon > b.MaxLon {
		b.MaxLon = lon
	}
	if lat < b.MinLat {
		b.MinLat = lat
	}
	if lat > b.MaxLat {
		b.MaxLat = lat
	}
	return b
}

// Inventory is an immutable, in-memory index over the EPT resources.
// Build one with NewFromBytes/Open/Refresh and share it across
// goroutines; all read methods are safe for concurrent use.
type Inventory struct {
	resources []Resource
	grid      *grid
	meta      Meta
}

// Open loads a cached inventory from dir without touching the network.
// dir must contain resources.geojson from a prior Refresh; meta.json is
// read if present.
func Open(dir string) (*Inventory, error) {
	data, err := os.ReadFile(filepath.Join(dir, dataFile))
	if err != nil {
		return nil, fmt.Errorf("no cached inventory in %s: %w (run inventory.Refresh first)", dir, err)
	}
	inv, err := NewFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("cached inventory in %s is corrupt: %w", dir, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, metaFile)); err == nil {
		_ = json.Unmarshal(data, &inv.meta)
	}
	return inv, nil
}

// Refresh fetches the inventory into dir with conditional-GET caching.
//
// First call (or after a source change) downloads the full file and
// writes it atomically; afterwards an If-None-Match/If-Modified-Since
// request returns 304 and the cache is reused. If the download fails,
// a previously cached inventory (if any) remains usable via Open.
func Refresh(ctx context.Context, dir string, c Client) (*Inventory, RefreshResult, error) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	src := c.SourceURL
	if src == "" {
		src = DefaultSource
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, RefreshResult{}, err
	}

	var oldMeta Meta
	if data, err := os.ReadFile(filepath.Join(dir, metaFile)); err == nil {
		_ = json.Unmarshal(data, &oldMeta)
	}
	oldNames := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(dir, dataFile)); err == nil {
		if inv, err := NewFromBytes(data); err == nil {
			for _, r := range inv.resources {
				oldNames[r.Name] = true
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, RefreshResult{}, err
	}
	if oldMeta.ETag != "" {
		req.Header.Set("If-None-Match", oldMeta.ETag)
	}
	if oldMeta.LastModified != "" {
		req.Header.Set("If-Modified-Since", oldMeta.LastModified)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, RefreshResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		inv, err := Open(dir)
		if err != nil {
			return nil, RefreshResult{}, fmt.Errorf("source reported not-modified but the cache is unusable: %w", err)
		}
		return inv, RefreshResult{Meta: oldMeta}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, RefreshResult{}, fmt.Errorf("unexpected status %s from %s", resp.Status, src)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, RefreshResult{}, err
	}
	// Validate before committing: a truncated or repurposed source must
	// not clobber a good cache.
	inv, err := NewFromBytes(body)
	if err != nil {
		return nil, RefreshResult{}, fmt.Errorf("fetched inventory failed validation: %w", err)
	}

	sum := sha256.Sum256(body)
	meta := Meta{
		ETag:         resp.Header.Get("Etag"),
		LastModified: resp.Header.Get("Last-Modified"),
		SHA256:       hex.EncodeToString(sum[:]),
		Source:       src,
		FetchedAt:    time.Now().UTC(),
		Count:        len(inv.resources),
	}
	if err := writeFileAtomic(filepath.Join(dir, dataFile), body); err != nil {
		return nil, RefreshResult{}, err
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, RefreshResult{}, err
	}
	if err := writeFileAtomic(filepath.Join(dir, metaFile), append(metaJSON, '\n')); err != nil {
		return nil, RefreshResult{}, err
	}
	inv.meta = meta

	res := RefreshResult{
		Changed: oldMeta.SHA256 == "" || oldMeta.SHA256 != meta.SHA256,
		Meta:    meta,
	}
	newNames := make(map[string]bool, len(inv.resources))
	for _, r := range inv.resources {
		newNames[r.Name] = true
		if !oldNames[r.Name] {
			res.Added = append(res.Added, r.Name)
		}
	}
	for name := range oldNames {
		if !newNames[name] {
			res.Removed = append(res.Removed, name)
		}
	}
	sort.Strings(res.Added)
	sort.Strings(res.Removed)
	return inv, res, nil
}

// NewFromBytes parses a resources.geojson FeatureCollection and builds an
// in-memory inventory.
func NewFromBytes(data []byte) (*Inventory, error) {
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Properties struct {
				Name  string `json:"name"`
				ID    int    `json:"id"`
				Count int64  `json:"count"`
				URL   string `json:"url"`
			} `json:"properties"`
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse inventory: %w", err)
	}
	if len(fc.Features) == 0 {
		return nil, fmt.Errorf("inventory has no features")
	}

	resources := make([]Resource, 0, len(fc.Features))
	for _, f := range fc.Features {
		rings, err := decodeRings(f.Geometry.Type, f.Geometry.Coordinates)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", f.Properties.Name, err)
		}
		if len(rings) == 0 {
			return nil, fmt.Errorf("resource %q has no geometry", f.Properties.Name)
		}
		resources = append(resources, Resource{
			Name:  f.Properties.Name,
			ID:    f.Properties.ID,
			Count: f.Properties.Count,
			URL:   f.Properties.URL,
			Year:  parseYear(f.Properties.Name),
			Box:   boxOfRings(rings),
			rings: rings,
		})
	}
	return &Inventory{resources: resources, grid: newGrid(resources)}, nil
}

// NewFromReader is NewFromBytes for a reader.
func NewFromReader(r io.Reader) (*Inventory, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return NewFromBytes(data)
}

// Resources returns the indexed resources. The slice and its elements
// must not be mutated.
func (inv *Inventory) Resources() []Resource { return inv.resources }

// Count is the number of indexed resources.
func (inv *Inventory) Count() int { return len(inv.resources) }

// Meta returns the cache metadata (zero value when the inventory was
// built without a cache, e.g. NewFromBytes).
func (inv *Inventory) Meta() Meta { return inv.meta }

// IsFullState reports whether a resource name is a state-wide
// "FullState" dataset, which rank above county-level projects.
func IsFullState(name string) bool { return strings.HasSuffix(name, "_FullState") }

var (
	fourDigitYear = regexp.MustCompile(`(?:19|20)[0-9]{2}`)
	batchCode     = regexp.MustCompile(`[A-Z]([0-9]{1,2})$`)
)

// parseYear extracts the acquisition year from a resource name. USGS
// project names carry it either as a 4-digit year
// ("AL_17Co_1_2020", "USGS_LPC_CA_Central_Valley_2017_LAS_2019") or,
// for batch acquisitions, as a trailing batch code
// ("AL_19Co_1_B24" -> 2024). The LAST 4-digit match wins when a name
// carries both a survey year and a LAS-ification year; a batch code is
// only consulted when no 4-digit year is present. Unknown -> 0, which
// sorts last.
func parseYear(name string) int {
	if ms := fourDigitYear.FindAllString(name, -1); len(ms) > 0 {
		v, _ := strconv.Atoi(ms[len(ms)-1])
		return v
	}
	if m := batchCode.FindStringSubmatch(name); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 1 && n <= 99 {
			return 2000 + n
		}
	}
	return 0
}

func decodeRings(geomType string, coords json.RawMessage) ([][][2]float64, error) {
	switch geomType {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(coords, &rings); err != nil {
			return nil, err
		}
		return rings, nil
	case "MultiPolygon":
		var mps [][][][2]float64
		if err := json.Unmarshal(coords, &mps); err != nil {
			return nil, err
		}
		var out [][][2]float64
		for _, poly := range mps {
			out = append(out, poly...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported geometry type %q", geomType)
	}
}

func boxOfRings(rings [][][2]float64) Box {
	b := Box{}
	first := true
	for _, ring := range rings {
		for _, p := range ring {
			if first {
				b = Box{p[0], p[1], p[0], p[1]}
				first = false
			} else {
				b = b.Extend(p[0], p[1])
			}
		}
	}
	return b
}

func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	name = ""
	return nil
}
