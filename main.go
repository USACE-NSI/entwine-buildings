// Command entwine-buildings extracts a USGS Entwine (EPT) point cloud for a
// parcel polygon, derives building footprints, heights, roof type and
// square footage, and writes a GeoJSON.
//
// Steps:
//  1. PDAL (docker): EPT -> parcel cloud (SMRF ground + HAG, DTM = Z - HAG,
//     original Classification preserved as SourceClass)
//  2. PDAL (docker): parcel cloud -> DSM/DTM grids (EPSG:4326)
//  3. PDAL (docker): parcel cloud -> clustered "building" points
//     (CSV with intensity / return / source-class columns, EPSG:4326)
//  4. In-process (go-gdal raster API only): cluster points -> PCA-oriented
//     box footprints; DSM/DTM grid sampled inside each box for height,
//     flat/pitched roof and slope.
//  5. Tree filtering: hull/box roundness + last-return + dark-intensity +
//     vegetation-class votes; components with enough votes are dropped.
//     -debug-components dumps per-building features for tuning.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gdal "github.com/usace-cloud-compute/go-gdal"
)

// ---------------------------------------------------------------------------
// CRS helpers. The only coordinate transform needed is WGS84 <-> EPSG:3857,
// closed-form spherical Mercator. The DSM/DTM grids are written in EPSG:4326
// so polygon vertices come straight from the affine GeoTransform.
// ---------------------------------------------------------------------------

const mercR = 6378137.0 // EPSG:3857 spherical radius

func wgs84To3857(lon, lat float64) (x, y float64) {
	const lim = 85.05112877980659
	if lat > lim {
		lat = lim
	} else if lat < -lim {
		lat = -lim
	}
	lr := lon * math.Pi / 180.0
	latr := lat * math.Pi / 180.0
	return mercR * lr, mercR * math.Log(math.Tan(math.Pi/4+latr/2))
}

// wgs84From3857 inverts wgs84To3857 (same closed-form spherical Mercator).
func wgs84From3857(x, y float64) (lon, lat float64) {
	lon = x / mercR * 180.0 / math.Pi
	lat = (2*math.Atan(math.Exp(y/mercR)) - math.Pi/2) * 180.0 / math.Pi
	return
}

// ---------------------------------------------------------------------------
// Parcel GeoJSON (WGS84)
// ---------------------------------------------------------------------------

type ring [][2]float64 // lon, lat
type gjGeom struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func loadParcelRings(path string) ([]ring, error) {
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
	var ringsOut []ring
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
			ringsOut = append(ringsOut, rs[0])
		case "MultiPolygon":
			var mps [][][][2]float64
			if err := json.Unmarshal(g.Coordinates, &mps); err != nil {
				return nil, err
			}
			for _, poly := range mps {
				if len(poly) > 0 {
					ringsOut = append(ringsOut, poly[0])
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

func ringBboxArea(r ring) float64 {
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

// wktRing renders a WGS84 ring as a WKT POLYGON in EPSG:3857 (for readers.ept).
func wktRing(r ring) string {
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
		x, y := wgs84To3857(p[0], p[1])
		fmt.Fprintf(&b, "%.4f %.4f", x, y)
	}
	b.WriteString("))")
	return b.String()
}

// ---------------------------------------------------------------------------
// PDAL orchestration
// ---------------------------------------------------------------------------

func runPDAL(image, volume, dockerWD, workdir, pipelineFile string) error {
	attempts := [][]string{
		{"pdal", "pipeline", dockerWD + "/" + pipelineFile, "--nostream"},
		{"pdal", "pipeline", dockerWD + "/" + pipelineFile}, // fallback for older PDAL without --nostream
	}
	mount := workdir + ":" + dockerWD
	if volume != "" {
		mount = volume + ":/" + dockerWD
	}
	var lastErr error
	for _, a := range attempts {
		args := append([]string{"run", "--rm", "-v", mount, "-w", dockerWD, image}, a...)
		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		lastErr = cmd.Run()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// ---------------------------------------------------------------------------
// Raster I/O via go-gdal (raster API only)
// ---------------------------------------------------------------------------

func readGrid(path string) (vals []float32, gt [6]float64, w, h int, err error) {
	ds, err := gdal.Open(path, gdal.ReadOnly)
	if err != nil {
		return nil, gt, 0, 0, err
	}
	defer ds.Close()
	w = ds.RasterXSize()
	h = ds.RasterYSize()
	gt = ds.GeoTransform()
	vals = make([]float32, w*h)
	ds.RasterBand(1).IO(gdal.Read, 0, 0, w, h, vals, w, h, 0, 0)
	return vals, gt, w, h, nil
}

const nodata = -9998.0 // grids use -9999; -9999 fails the ">" test

// ---------------------------------------------------------------------------
// Cluster points CSV (writers.text: header line +
// "X,Y,Z,ClusterID,Intensity,ReturnNumber,NumberOfReturns,SourceClass")
// ---------------------------------------------------------------------------

type bpt struct {
	lon, lat, z float64
	cluster     int
	intensity   float64 // 0 if column missing
	retNum      int     // 0 if column missing
	numRet      int
	srcClass    int // original ASPRS class, 0 if column missing
}

func loadPoints(path string) []bpt {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var pts []bpt
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		fld := strings.Split(sc.Text(), ",")
		if lineNo == 1 || len(fld) < 3 {
			continue
		}
		x, e1 := strconv.ParseFloat(fld[0], 64)
		y, e2 := strconv.ParseFloat(fld[1], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		z := 0.0
		if len(fld) > 2 {
			z, _ = strconv.ParseFloat(fld[2], 64)
		}
		c := 0
		if len(fld) > 3 {
			c, _ = strconv.Atoi(fld[3])
		}
		var p bpt
		p.lon, p.lat, p.z, p.cluster = x, y, z, c
		if len(fld) > 4 {
			p.intensity, _ = strconv.ParseFloat(fld[4], 64)
		}
		if len(fld) > 5 {
			p.retNum, _ = strconv.Atoi(fld[5])
		}
		if len(fld) > 6 {
			p.numRet, _ = strconv.Atoi(fld[6])
		}
		if len(fld) > 7 {
			p.srcClass, _ = strconv.Atoi(fld[7])
		}
		pts = append(pts, p)
	}
	return pts
}

// ---------------------------------------------------------------------------
// Geometry helpers (projected meter coordinates, EPSG:3857)
// ---------------------------------------------------------------------------

// pcaAngle: orientation (rad) of the principal axis, centroid, and the
// eigenvalue ratio (>=1) of a 2-D point set. ratio ~1 => near-square.
func pcaAngle(pts [][2]float64) (angle, cx, cy, evRatio float64) {
	n := len(pts)
	if n == 0 {
		return
	}
	cx, cy = 0, 0
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	cx /= float64(n)
	cy /= float64(n)
	a, b, d := 0.0, 0.0, 0.0 // cov00, cov01, cov11
	for _, p := range pts {
		dx, dy := p[0]-cx, p[1]-cy
		a += dx * dx
		b += dx * dy
		d += dy * dy
	}
	a /= float64(n)
	b /= float64(n)
	d /= float64(n)
	half := (a - d) / 2
	lam1 := (a+d)/2 + math.Sqrt(half*half+b*b)
	lam2 := (a+d)/2 - math.Sqrt(half*half+b*b)
	if lam2 < 1e-9 {
		lam2 = 1e-9
	}
	evRatio = lam1 / lam2
	angle = 0.5 * math.Atan2(2*b, a-d)
	return
}

// box2 is a rectangle aligned to a given bearing, in projected (meter) coords.
type box2 struct {
	Corners [4][2]float64 // (minx,miny),(maxx,miny),(maxx,maxy),(minx,maxy) in the rotated frame
	Angle   float64       // long-axis bearing (rad)
	Long    float64
	Short   float64
}

func boxArea(b box2) float64 { return b.Long * b.Short }

// boxAt returns the oriented bounding box of pts around the given bearing.
func boxAt(pts [][2]float64, ang float64) box2 {
	cx, cy := 0.0, 0.0
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	n := float64(len(pts))
	cx /= n
	cy /= n
	ca, sa := math.Cos(ang), math.Sin(ang)
	minx, miny, maxx, maxy := math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64
	for _, p := range pts {
		dx, dy := p[0]-cx, p[1]-cy
		rx := dx*ca + dy*sa // rotate into the rotated frame
		ry := -dx*sa + dy*ca
		if rx < minx {
			minx = rx
		}
		if rx > maxx {
			maxx = rx
		}
		if ry < miny {
			miny = ry
		}
		if ry > maxy {
			maxy = ry
		}
	}
	lo := func(x, y float64) [2]float64 {
		return [2]float64{cx + x*ca - y*sa, cy + x*sa + y*ca}
	}
	return box2{
		Corners: [4][2]float64{
			lo(minx, miny), lo(maxx, miny), lo(maxx, maxy), lo(minx, maxy),
		},
		Angle: ang,
		Long:  maxx - minx,
		Short: maxy - miny,
	}
}

// pointInQuad: ray cast against a 4-corner box.
func pointInQuad(x, y float64, q [4][2]float64) bool {
	inside := false
	for i := 0; i < 4; i++ {
		a, b := q[i], q[(i+1)%4]
		if (a[1] > y) != (b[1] > y) {
			xint := a[0] + (y-a[1])*(b[0]-a[0])/(b[1]-a[1])
			if x < xint {
				inside = !inside
			}
		}
	}
	return inside
}

// boxOverlapFrac: fraction of the smaller box covered by the intersection,
// approximated with axis-aligned bounding boxes.
func boxOverlapFrac(a, b box2) float64 {
	abb := func(b box2) (minx, miny, maxx, maxy float64) {
		minx, miny, maxx, maxy = b.Corners[0][0], b.Corners[0][1], b.Corners[0][0], b.Corners[0][1]
		for _, c := range b.Corners {
			if c[0] < minx {
				minx = c[0]
			}
			if c[0] > maxx {
				maxx = c[0]
			}
			if c[1] < miny {
				miny = c[1]
			}
			if c[1] > maxy {
				maxy = c[1]
			}
		}
		return
	}
	ax0, ay0, ax1, ay1 := abb(a)
	bx0, by0, bx1, by1 := abb(b)
	ix0, iy0 := math.Max(ax0, bx0), math.Max(ay0, by0)
	ix1, iy1 := math.Min(ax1, bx1), math.Min(ay1, by1)
	if ix1 <= ix0 || iy1 <= iy0 {
		return 0
	}
	inter := (ix1 - ix0) * (iy1 - iy0)
	smaller := math.Min(boxArea(a), boxArea(b))
	if smaller <= 0 {
		return 0
	}
	return inter / smaller
}

// convexHull: Andrew's monotone chain. Returns the hull CCW, not repeated.
func convexHull(pts [][2]float64) [][2]float64 {
	n := len(pts)
	if n < 3 {
		return pts
	}
	p := append([][2]float64(nil), pts...)
	sort.Slice(p, func(i, j int) bool {
		if p[i][0] != p[j][0] {
			return p[i][0] < p[j][0]
		}
		return p[i][1] < p[j][1]
	})
	cross := func(o, a, b [2]float64) float64 {
		return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
	}
	hull := make([][2]float64, 0, n)
	for _, q := range p {
		for len(hull) >= 2 && cross(hull[len(hull)-2], hull[len(hull)-1], q) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, q)
	}
	upper := len(hull)
	for i := n - 1; i >= 0; i-- {
		q := p[i]
		for len(hull) >= upper+1 && cross(hull[len(hull)-2], hull[len(hull)-1], q) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, q)
	}
	if len(hull) > 1 {
		hull = hull[:len(hull)-1]
	}
	return hull
}

func hullArea(h [][2]float64) float64 {
	if len(h) < 3 {
		return 0
	}
	a := 0.0
	for i := 0; i < len(h); i++ {
		p, q := h[i], h[(i+1)%len(h)]
		a += p[0]*q[1] - q[0]*p[1]
	}
	return math.Abs(a) / 2
}

func percentile(vals []float64, p float64) float64 {
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

// ---------------------------------------------------------------------------
// Roof classification from DSM-DTM cells inside a footprint
// ---------------------------------------------------------------------------

type roofInfo struct {
	Kind     string  // "flat", "pitched", or "unknown"
	SlopeDeg float64 // 0 for flat/unknown
	RidgeDeg float64 // ridge bearing (deg from north) when the top band is a line
	Relief   float64 // zmax - zmin over the roof cells
}

// widthPerp: the box dimension perpendicular to a ridge bearing. If the ridge
// runs along the box's long axis, the rise spans the short side, and vice
// versa.
func widthPerp(b box2, ang float64) float64 {
	d := math.Mod(math.Mod(ang-b.Angle, math.Pi)+math.Pi, math.Pi)
	if d > math.Pi/2 {
		d = math.Pi - d
	}
	if d < math.Pi/4 {
		return b.Short
	}
	return b.Long
}

// classifyRoof: cells are (x, y, heightAboveGround) in projected meters.
func classifyRoof(cells [][3]float64, b box2, flatRelief float64) roofInfo {
	if len(cells) == 0 {
		return roofInfo{Kind: "unknown"}
	}
	zmax, zmin := math.Inf(-1), math.Inf(1)
	for _, c := range cells {
		if c[2] > zmax {
			zmax = c[2]
		}
		if c[2] < zmin {
			zmin = c[2]
		}
	}
	relief := zmax - zmin
	if relief < flatRelief {
		return roofInfo{Kind: "flat", Relief: relief}
	}
	// Top band: cells in the top 30% of the roof relief.
	cut := zmax - 0.3*relief
	var top [][2]float64
	for _, c := range cells {
		if c[2] >= cut {
			top = append(top, [2]float64{c[0], c[1]})
		}
	}
	var slopeDeg, ridgeDeg float64
	if len(top) >= 4 {
		ang, _, _, evR := pcaAngle(top)
		if evR > 3 {
			// Top band is a line: a ridge running the length (gable).
			ridgeDeg = ang * 180 / math.Pi
			perp := widthPerp(b, ang)
			if perp > 0.5 {
				slopeDeg = math.Atan(relief/(perp/2)) * 180 / math.Pi
			}
		} else {
			// Top band is a blob: peak (hip). Rise spans the short side.
			if b.Short > 0.5 {
				slopeDeg = math.Atan(relief/(b.Short/2)) * 180 / math.Pi
			}
		}
	} else if b.Short > 0.5 {
		slopeDeg = math.Atan(relief/(b.Short/2)) * 180 / math.Pi
	}
	if slopeDeg > 60 {
		slopeDeg = 60
	}
	return roofInfo{Kind: "pitched", SlopeDeg: slopeDeg, RidgeDeg: ridgeDeg, Relief: relief}
}

// chooseAngle picks the box bearing for a building: the roof-ridge bearing
// (top band) when the ridge signal is strong, else full-cloud PCA with the
// near-square parcel snap. Second return is the source: "ridge", "pca",
// or "pca-snap".
func chooseAngle(ll [][2]float64, pts []bpt, useRidge bool, snap float64, parcelAng float64, parcelKnown bool) (float64, string) {
	if useRidge {
		if a, ok := ridgeBearing(pts); ok {
			return a, "ridge"
		}
	}
	ang, _, _, evR := pcaAngle(ll)
	if snap > 0 && parcelKnown && evR < snap {
		return snapToParcelAngle(ang, parcelAng), "pca-snap"
	}
	return ang, "pca"
}

// normOrientDeg folds a PCA/ridge angle (rad, any range) into 0..180 deg.
func normOrientDeg(a float64) float64 {
	d := math.Mod(a*180/math.Pi, 180)
	if d < 0 {
		d += 180
	}
	return d
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

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

type gjGeometry struct {
	Type        string `json:"type"`
	Coordinates []ring `json:"coordinates"` // ring = [][2]float64
}

type gjFeature struct {
	Type       string                 `json:"type"`
	Geometry   gjGeometry             `json:"geometry"`
	Properties map[string]interface{} `json:"properties"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "entwine-buildings: "+format+"\n", a...)
	os.Exit(1)
}

// ridgeBearing estimates the ridge/long-axis bearing (radians, modulo pi) from
// the cluster's TOP BAND only — the roof ridge for gable roofs. Tree tops are
// rare in the cloud, so the upper roof surface is the cleanest orientation
// signal we have. Returns ok=false when the signal is too weak (flat roof,
// too few points, blob-like hip apex) and the caller should fall back to
// full-cloud PCA.
func ridgeBearing(pts []bpt) (float64, bool) {
	if len(pts) < 6 {
		return 0, false
	}
	m := make([][2]float64, len(pts))
	zs := make([]float64, len(pts))
	for i := range pts {
		x, y := wgs84To3857(pts[i].lon, pts[i].lat)
		m[i] = [2]float64{x, y}
		zs[i] = pts[i].z
	}
	sorted := append([]float64(nil), zs...)
	sort.Float64s(sorted)
	zmin := sorted[0]
	// p90, not zmax: a few fused tree tops would otherwise corrupt the cut.
	zRidge := pctlFloats(sorted, 0.90)
	relief := zRidge - zmin
	if relief < 1.0 { // flat roof: no ridge to orient on
		return 0, false
	}
	cut := zRidge - 0.30*relief // top 30% of the roof surface
	var band [][2]float64
	for i := range pts {
		if pts[i].z >= cut {
			band = append(band, m[i])
		}
	}
	if len(band) < 5 {
		return 0, false
	}
	a, cx, cy, _ := pcaAngle(band)
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

// pctlFloats: quantile of an ALREADY-SORTED float slice (0..1).
func pctlFloats(sorted []float64, q float64) float64 {
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

// orientedBBoxAt: corners of the cluster's oriented box, aligned to an
// externally supplied angle (radians; the 180 deg ambiguity is irrelevant
// to a rectangle).
func orientedBBoxAt(pts [][2]float64, angle float64) [4][2]float64 {
	if len(pts) == 0 {
		return [4][2]float64{}
	}
	var cx, cy float64
	for _, p := range pts {
		cx += p[0]
		cy += p[1]
	}
	cx /= float64(len(pts))
	cy /= float64(len(pts))
	cosA, sinA := math.Cos(angle), math.Sin(angle)
	minU, maxU := math.Inf(1), math.Inf(-1)
	minV, maxV := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
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
	corners := [4][2]float64{{minU, minV}, {maxU, minV}, {maxU, maxV}, {minU, maxV}}
	out := [4][2]float64{}
	for i := range corners {
		u, v := corners[i][0], corners[i][1]
		out[i] = [2]float64{cx + u*cosA - v*sinA, cy + u*sinA + v*cosA}
	}
	return out
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	parcel := flag.String("parcel", "", "parcel GeoJSON (WGS84), required")
	workdir := flag.String("workdir", ".", "scratch dir for intermediate files")
	eptJSON := flag.String("ept-json",
		"https://s3-us-west-2.amazonaws.com/usgs-lidar-public/IA_FullState/ept.json",
		"EPT ept.json URL")
	threshold := flag.Float64("threshold", 4.0, "min height above ground (m) for cluster points")
	minPoints := flag.Int("min-points", 3, "min cluster points per building (after decimation)")
	resolution := flag.Float64("resolution", 0.00001, "grid resolution in degrees (~1 m at 42N)")
	pdalImage := flag.String("pdal-image", "pdal/pdal", "PDAL docker image")
	skipPDAL := flag.Bool("skip-pdal", false, "skip PDAL docker steps (reuse existing outputs)")
	dockerVolume := flag.String("docker-volume", "",
		"named docker volume for PDAL scratch files (e.g. entwine-work). When set, -workdir must be the volume's mount point (e.g. /work). Empty = bind-mount -workdir at the same path (native Linux).")
	dockerWorkdir := flag.String("docker-workdir", "",
		"workdir path inside the PDAL container (default: /work when -docker-volume is set, else -workdir).")
	dropTrees := flag.Bool("drop-trees", true, "drop components that vote as tree canopy")
	treeVoteMin := flag.Int("tree-vote-min", 2, "tree votes required to drop a component; 0 disables tree filtering")
	hullRound := flag.Float64("hull-round", 0.85, "convex-hull/box area ratio < this earns a 'round' tree vote")
	treeIntensityPct := flag.Float64("tree-intensity-pct", 25,
		"dark-intensity cut as a percentile of building mean intensities (0 disables the dark vote)")
	treeLastRet := flag.Float64("tree-lastret", 0.35, "last-return fraction < this earns a 'penetrating canopy' vote")
	flatRelief := flag.Float64("flat-relief", 0.75, "roof relief (m) below which the roof is classified flat")
	snapToParcel := flag.Float64("snap-to-parcel", 1.25,
		"if a building's PCA eigenvalue ratio is below this, snap its box to the parcel's long axis (0 disables)")
	orientRidge := flag.Bool("orient-ridge", true,
		"orient boxes by roof-ridge bearing (top band of the cluster point cloud); falls back to full-cloud PCA when the ridge signal is weak. Disable with -orient-ridge=false")
	debugComps := flag.Bool("debug-components", false, "write components.csv with per-building features for threshold tuning")
	flag.Parse()

	if *parcel == "" {
		fatal("-parcel parcel.geojson is required")
	}
	wd, err := filepath.Abs(*workdir)
	if err != nil {
		fatal("workdir: %v", err)
	}
	if err := os.MkdirAll(wd, 0755); err != nil {
		fatal("workdir: %v", err)
	}
	dockerWD := *dockerWorkdir
	if dockerWD == "" {
		if *dockerVolume != "" {
			dockerWD = "/work"
		} else {
			dockerWD = wd
		}
	}
	rings, err := loadParcelRings(*parcel)
	if err != nil {
		fatal("parcel: %v", err)
	}

	// EPT fetch filter: largest outer ring, WKT in EPSG:3857.
	mainRing, maxA := rings[0], 0.0
	for _, r := range rings {
		if a := ringBboxArea(r); a > maxA {
			maxA, mainRing = a, r
		}
	}
	polyWKT := wktRing(mainRing)

	// Parcel orientation prior: PCA long axis of the main ring.
	parcelAng := 0.0
	parcelAngKnown := false
	if len(mainRing) >= 4 {
		prj := make([][2]float64, 0, len(mainRing))
		for _, p := range mainRing {
			x, y := wgs84To3857(p[0], p[1])
			prj = append(prj, [2]float64{x, y})
		}
		parcelAng, _, _, _ = pcaAngle(prj)
		parcelAngKnown = true
	}

	// Grid bounds in EPSG:4326: parcel bbox + ~20 m margin.
	minLon, minLat, maxLon, maxLat := 180.0, 90.0, -180.0, -90.0
	for _, r := range rings {
		for _, p := range r {
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
	latMid := (minLat + maxLat) / 2
	mLat := 20.0 / 111320.0
	mLon := 20.0 / (111320.0 * math.Cos(latMid*math.Pi/180))
	boundsS := fmt.Sprintf("([%.8f, %.8f], [%.8f, %.8f])",
		minLon-mLon, maxLon+mLon, // [xmin, xmax]
		minLat-mLat, maxLat+mLat) // [ymin, ymax]
	resS := strconv.FormatFloat(*resolution, 'f', 8, 64)
	radS := strconv.FormatFloat(*resolution*4, 'f', 8, 64)
	thrS := strconv.FormatFloat(*threshold, 'f', 3, 64)

	pipeline1 := fmt.Sprintf(`[
  {"type":"readers.ept","filename":"%s","polygon":"%s","requests":8},
  {"type":"filters.reprojection","out_srs":"EPSG:26915"},
  {"type":"filters.expression","expression":"Classification != 7 && Classification != 12 && Classification != 18"},
  {"type":"filters.assign","value":"SourceClass = Classification"},
  {"type":"filters.smrf","cell":2,"slope":0.25,"window":21},
  {"type":"filters.hag_nn","count":8,"max_distance":10,"allow_extrapolation":false,"class":2},
  {"type":"filters.assign","value":"DTM = Z - HeightAboveGround"},
  {"type":"writers.las","filename":"%s/parcel_cloud.laz","extra_dims":["DTM=Double","SourceClass=Double"]}
]`, *eptJSON, polyWKT, dockerWD)

	pipeline2 := fmt.Sprintf(`[
  {"type":"readers.las","filename":"%s/parcel_cloud.laz"},
  {"type":"filters.reprojection","out_srs":"EPSG:4326"},
  {"type":"writers.gdal","filename":"%s/dsm.tif","dimension":"Z","output_type":"max","binmode":true,"resolution":%s,"bounds":"%s","gdaldriver":"GTiff","data_type":"Float32","nodata":-9999,"window_size":1},
  {"type":"writers.gdal","filename":"%s/dtm.tif","dimension":"DTM","output_type":"idw","radius":%s,"power":2,"resolution":%s,"bounds":"%s","gdaldriver":"GTiff","data_type":"Float32","nodata":-9999,"window_size":1}
]`, dockerWD, dockerWD, resS, boundsS, dockerWD, radS, resS, boundsS)

	pipeline3 := fmt.Sprintf(`[
  {"type":"readers.las","filename":"%s/parcel_cloud.laz"},
  {"type":"filters.expression","expression":"Z-DTM > %s"},
  {"type":"filters.cluster","min_points":30,"tolerance":3},
  {"type":"filters.expression","expression":"ClusterID > 0"},
  {"type":"filters.decimation","step":5},
  {"type":"filters.reprojection","out_srs":"EPSG:4326"},
  {"type":"writers.text","filename":"%s/buildings_points.csv","order":"X:8,Y:8,Z:2,ClusterID:0,Intensity:0,ReturnNumber:0,NumberOfReturns:0,SourceClass:0","keep_unspecified":"false"}
]`, dockerWD, thrS, dockerWD)

	for name, content := range map[string]string{
		"pipeline1.json": pipeline1,
		"pipeline2.json": pipeline2,
		"pipeline3.json": pipeline3,
	} {
		if err := os.WriteFile(filepath.Join(wd, name), []byte(content), 0644); err != nil {
			fatal("writing %s: %v", name, err)
		}
	}

	if !*skipPDAL {
		if err := runPDAL(*pdalImage, *dockerVolume, dockerWD, wd, "pipeline1.json"); err != nil {
			fatal("pdal pipeline1 (EPT -> parcel cloud): %v", err)
		}
		fmt.Println("== pipeline1 done")
		if err := runPDAL(*pdalImage, *dockerVolume, dockerWD, wd, "pipeline2.json"); err != nil {
			fatal("pdal pipeline2 (grids): %v", err)
		}
		fmt.Println("== pipeline2 done")
		if err := runPDAL(*pdalImage, *dockerVolume, dockerWD, wd, "pipeline3.json"); err != nil {
			fatal("pdal pipeline3 (building points): %v", err)
		}
		fmt.Println("== pipeline3 done")
	}

	// --- Grids (height + roof sampling) ---
	dsm, gt, w, h, err := readGrid(filepath.Join(wd, "dsm.tif"))
	if err != nil {
		fatal("reading dsm.tif: %v", err)
	}
	dtm, _, _, _, err := readGrid(filepath.Join(wd, "dtm.tif"))
	if err != nil {
		fatal("reading dtm.tif: %v", err)
	}
	fmt.Printf("== grids: %dx%d px, resolution %.8f deg\n", w, h, gt[1])

	// --- Cluster points -> buildings ---
	bpts := loadPoints(filepath.Join(wd, "buildings_points.csv"))
	fmt.Printf("== %d cluster points\n", len(bpts))

	byCID := map[int][]bpt{}
	for _, p := range bpts {
		byCID[p.cluster] = append(byCID[p.cluster], p)
	}

	type cluster struct {
		id  int
		pts []bpt
		ll  [][2]float64 // projected meters
	}
	var clusters []cluster
	for cid, pts := range byCID {
		if len(pts) < *minPoints {
			continue
		}
		ll := make([][2]float64, len(pts))
		for j, p := range pts {
			x, y := wgs84To3857(p.lon, p.lat)
			ll[j] = [2]float64{x, y}
		}
		clusters = append(clusters, cluster{id: cid, pts: pts, ll: ll})
	}

	// Merge clusters whose boxes overlap (one roof split into two clusters).
	boxes := make([]box2, len(clusters))
	for i := range clusters {
		ang, _, _, _ := pcaAngle(clusters[i].ll)
		boxes[i] = boxAt(clusters[i].ll, ang)
	}
	type group struct {
		ll  [][2]float64
		pts []bpt
	}
	groups := make([]group, 0, len(clusters))
	used := make([]bool, len(clusters))
	for i := 0; i < len(clusters); i++ {
		if used[i] {
			continue
		}
		g := group{
			ll:  append([][2]float64(nil), clusters[i].ll...),
			pts: append([]bpt(nil), clusters[i].pts...),
		}
		for j := i + 1; j < len(clusters); j++ {
			if used[j] || boxOverlapFrac(boxes[i], boxes[j]) <= 0.25 {
				continue
			}
			used[j] = true
			g.ll = append(g.ll, clusters[j].ll...)
			g.pts = append(g.pts, clusters[j].pts...)
		}
		used[i] = true
		groups = append(groups, g)
	}
	if len(groups) < len(clusters) {
		fmt.Printf("== merged %d overlapping clusters\n", len(clusters)-len(groups))
	}

	// Per-building: oriented box, height, roof, point aggregates, tree votes.
	type bld struct {
		g                            group
		bx                           box2
		corners                      [][2]float64 // lon/lat, closed
		area                         float64
		height                       float64
		roof                         roofInfo
		hullFrac                     float64
		meanInt                      float64
		lastRetFrac                  float64
		bldFrac, vegFrac             float64
		intKnown, retKnown, clsKnown bool
		votes                        int
		kept                         bool
		reason                       string
		angSrc                       string
	}
	var blds []bld
	for _, g := range groups {
		ang, angSrc := chooseAngle(g.ll, g.pts, *orientRidge, *snapToParcel, parcelAng, parcelAngKnown)
		bx := boxAt(g.ll, ang)
		area := boxArea(bx)
		corners := make([][2]float64, 0, 5)
		for _, c := range bx.Corners {
			lon, lat := wgs84From3857(c[0], c[1])
			corners = append(corners, [2]float64{lon, lat})
		}
		corners = append(corners, corners[0])

		// Height + roof from the DSM/DTM grid inside the box.
		var cells [][3]float64
		for py := 0; py < h; py++ {
			for px := 0; px < w; px++ {
				lon := gt[0] + (float64(px)+0.5)*gt[1] + (float64(py)+0.5)*gt[2]
				lat := gt[3] + (float64(px)+0.5)*gt[4] + (float64(py)+0.5)*gt[5]
				x, y := wgs84To3857(lon, lat)
				if !pointInQuad(x, y, bx.Corners) {
					continue
				}
				i := py*w + px
				if dsm[i] <= nodata || dtm[i] <= nodata {
					continue
				}
				v := float64(dsm[i] - dtm[i])
				if v < 0.05 {
					continue
				}
				cells = append(cells, [3]float64{x, y, v})
			}
		}
		height := 0.0
		roof := roofInfo{Kind: "unknown"}
		if len(cells) > 0 {
			vals := make([]float64, len(cells))
			for i, c := range cells {
				vals[i] = c[2]
			}
			height = percentile(vals, 0.9)
			roof = classifyRoof(cells, bx, *flatRelief)
		}

		// Point aggregates (votes).
		// Point aggregates (votes).
		var bl bld
		bl.g, bl.bx, bl.corners, bl.area, bl.height, bl.roof = g, bx, corners, area, height, roof
		bl.angSrc = angSrc
		hull := convexHull(g.ll)
		if area > 0 {
			bl.hullFrac = hullArea(hull) / area
		}
		sumInt, nInt := 0.0, 0
		nRet, lastRet, nCls, nBld, nVeg := 0, 0, 0, 0, 0
		for _, p := range g.pts {
			if p.intensity > 0 {
				sumInt += p.intensity
				nInt++
			}
			if p.numRet > 0 {
				nRet++
				if p.retNum == p.numRet {
					lastRet++
				}
			}
			if p.srcClass > 0 {
				nCls++
				switch p.srcClass {
				case 6: // building
					nBld++
				case 3, 4, 5: // low/medium/high vegetation
					nVeg++
				}
			}
		}
		bl.intKnown = nInt > 0
		bl.retKnown = nRet > 0
		bl.clsKnown = nCls > 0
		if nInt > 0 {
			bl.meanInt = sumInt / float64(nInt)
		}
		if nRet > 0 {
			bl.lastRetFrac = float64(lastRet) / float64(nRet)
		}
		if nCls > 0 {
			bl.bldFrac = float64(nBld) / float64(nCls)
			bl.vegFrac = float64(nVeg) / float64(nCls)
		}
		blds = append(blds, bl)
	}

	// Auto dark-intensity cut: percentile of the building mean intensities.
	intCut := 0.0
	if *dropTrees && *treeIntensityPct > 0 {
		var ints []float64
		for _, b := range blds {
			if b.intKnown {
				ints = append(ints, b.meanInt)
			}
		}
		if len(ints) >= 4 {
			intCut = percentile(ints, *treeIntensityPct/100)
		}
	}

	// Tree votes (of 4): round hull, dark, penetrating, vegetation class.
	droppedTrees := 0
	for i := range blds {
		b := &blds[i]
		if !*dropTrees || *treeVoteMin <= 0 {
			b.kept = true
			continue
		}
		v := 0
		if b.hullFrac > 0 && b.hullFrac < *hullRound {
			v++ // round in plan
		}
		if b.intKnown && intCut > 0 && b.meanInt < intCut {
			v++ // dark
		}
		if b.retKnown && b.lastRetFrac < *treeLastRet {
			v++ // penetrating canopy
		}
		if b.clsKnown && b.vegFrac > 0.5 && b.bldFrac < 0.1 {
			v++ // vegetation class votes
		}
		b.votes = v
		if v >= *treeVoteMin {
			b.kept = false
			b.reason = "tree"
			droppedTrees++
		} else {
			b.kept = true
		}
	}
	if droppedTrees > 0 {
		fmt.Printf("== dropped %d tree-like building(s)\n", droppedTrees)
	}

	if *debugComps {
		var b strings.Builder
		b.WriteString("id,area_m2,height_m,relief,roof,slope_deg,ridge_deg,orient,orient_deg,hull_frac,lastret_frac,mean_intensity,bld_frac,veg_frac,tree_votes,kept,reason\n")
		for i, bl := range blds {
			fmt.Fprintf(&b, "B%d,%.1f,%.2f,%.2f,%s,%.1f,%.1f,%s,%.1f,%.3f,%.3f,%.0f,%.3f,%.3f,%d,%t,%s\n",
				i+1, bl.area, bl.height, bl.roof.Relief, bl.roof.Kind, bl.roof.SlopeDeg, bl.roof.RidgeDeg,
				bl.angSrc, normOrientDeg(bl.bx.Angle),
				bl.hullFrac, bl.lastRetFrac, bl.meanInt, bl.bldFrac, bl.vegFrac, bl.votes, bl.kept, bl.reason)
		}
		cpath := filepath.Join(wd, "components.csv")
		if err := os.WriteFile(cpath, []byte(b.String()), 0644); err != nil {
			fatal("writing components.csv: %v", err)
		}
		fmt.Printf("== wrote %d building rows to %s\n", len(blds), cpath)
	}

	kept := make([]bld, 0, len(blds))
	for _, b := range blds {
		if b.kept {
			kept = append(kept, b)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].area > kept[j].area })

	fc := struct {
		Type     string      `json:"type"`
		Features []gjFeature `json:"features"`
	}{Type: "FeatureCollection"}
	for i, b := range kept {
		fc.Features = append(fc.Features, gjFeature{
			Type: "Feature",
			Geometry: gjGeometry{
				Type:        "Polygon",
				Coordinates: []ring{b.corners},
			},
			Properties: map[string]interface{}{
				"id":             fmt.Sprintf("B%d", i+1),
				"roof":           b.roof.Kind,
				"slope_deg":      round1(b.roof.SlopeDeg),
				"ridge_deg":      round1(b.roof.RidgeDeg),
				"height_m":       round2(b.height),
				"area_m2":        round1(b.area),
				"area_sqft":      round1(b.area / 0.09290304),
				"points":         len(b.g.pts),
				"tree_votes":     b.votes,
				"potential_tree": !b.kept,
				"method":         "ept-dsm-dtm-box",
			},
		})
	}

	outPath := filepath.Join(wd, "buildings.geojson")
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		fatal("marshaling GeoJSON: %v", err)
	}
	if err := os.WriteFile(outPath, data, 0600); err != nil {
		fatal("writing %s: %v", outPath, err)
	}
	fmt.Printf("== wrote %d building(s) to %s\n", len(fc.Features), outPath)
}
