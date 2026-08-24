# entwine-buildings

Extracts building footprints (oriented-box geometry + `height_m` + `roof` +
`slope_deg` + `area_m2` + `area_sqft`) from the USGS 3DEP Entwine Point Tile
(EPT) LiDAR archive for a parcel polygon, using three PDAL stages in Docker
and an in-process Go runner (go-gdal, raster API only).

## Requirements

- Docker, with the `pdal/pdal` image (`docker pull pdal/pdal`)
- Go 1.27+ to build the runner (depends on the `usace-cloud-compute/go-gdal`
  fork, which links against a system GDAL C library)
- A parcel GeoJSON in WGS84 lon/lat (Polygon / MultiPolygon / Feature /
  FeatureCollection)

## Quickstart

```bash
go build -o entwine-buildings .

# Full run: PDAL stages + Go analysis
./entwine-buildings -parcel example/res-parcel3.geojson \
  -workdir /work -docker-volume entwine-work

# Go-only iteration (reuse grids + CSV already in the workdir)
./entwine-buildings -parcel example/res-parcel3.geojson \
  -workdir /work -skip-pdal -debug-components
```
-docker-volume names a Docker named volume mounted at /work inside the
pdal/pdal image (use -workdir as its host mount point). Without it, the
workdir is bind-mounted at the same path on a native Linux host.

Example parcels live in example/ (parcel.geojson, res-parcel*.geojson,
plus QGIS project files *.qmd for visual checks).

## Outputs (in the workdir)
|file	|what|
|---|---|
|buildings.geojson	|final: WGS84 oriented-box footprints with id, roof, slope_deg, ridge_deg, height_m, area_m2, area_sqft, points, tree_votes, potential_tree, method|
|parcel_cloud.laz	|parcel point cloud in EPSG:26915 with DTM and SourceClass dims|
|dsm.tif / dtm.tif	|EPSG:4326 grids (∼1 m cells at 42N), DSM = max Z, DTM = IDW of ground|
|buildings_points.csv	|decimated clustered points, EPSG:4326: X,Y,Z,ClusterID,Intensity,ReturnNumber,NumberOfReturns,SourceClass|
|components.csv	|per-building features for tuning (only with -debug-components)
pipeline1.json, pipeline2.json, pipeline3.json	generated PDAL pipelines, inspectable|

## Flags (all have sensible defaults)

|flag	|default|	meaning|
|--|--|---|
|-parcel|	— (required)|	parcel GeoJSON, WGS84 lon/lat|
|-workdir|	.|	output directory|
|-ept-json|	IA_FullState/ept.json|	any USGS 3DEP state EPT URL|
|-threshold|	4.0|	min height above DTM (m) for a point to be a building point|
|-min-points|	3|	min cluster points per building (after decimation)|
|-resolution|	0.00001|	DSM/DTM cell size in degrees (∼1 m at 42N)|
|-pdal-image|	pdal/pdal|	Docker image for the PDAL steps|
|-skip-pdal|	false|	skip the Docker stages; reuse existing grids + CSV|
|-docker-volume|	—|	named Docker volume mounted at the docker workdir|
|-docker-workdir|	/work| (with volume)	workdir path inside the PDAL container|
|-drop-trees|	true|	drop components that vote as tree canopy|
|-tree-vote-min|	2|	tree votes required to drop a component (0 disables)|
|-hull-round|	0.85|	convex-hull/box area ratio below this earns a "round" tree vote|
|-tree-intensity-pct|	25|	dark-intensity cut as a percentile of building mean intensities|
|-tree-lastret|	0.35|	last-return fraction below this earns a "penetrating canopy" vote|
|-flat-relief|	0.75|	roof relief (m) below which the roof is classified flat|
|-snap-to-parcel|	1.25|	if the building's PCA eigenvalue ratio is below this, snap the box to the parcel's long axis (0 disables)|
|-orient-ridge|	true|	orient boxes by the roof-ridge bearing (top 30% band of the cluster, P90 cut) when the band is line-like; otherwise fall back to full-cloud PCA|
|-debug-components|	false|	write components.csv with per-building features|


## How it works

Go runner reprojects the largest parcel ring to EPSG:3857 with closed-form spherical Mercator (the EPT's storage CRS), and computes a parcel long-axis bearing (PCA of the ring) as a snap prior.

PDAL pipeline 1 queries the EPT with the parcel polygon, reprojects to EPSG:26915, drops noise (ASPRS classes 7/12/18), preserves the original Classification as SourceClass, runs SMRF ground + nearest-neighbor HAG, sets DTM = Z - HAG, and writes parcel_cloud.laz.

PDAL pipeline 2 reprojects to EPSG:4326 and writes dsm.tif (max Z, binned) and dtm.tif (IDW of the DTM) at -resolution over the parcel bbox + ∼20 m margin.

PDAL pipeline 3 keeps points with Z - DTM > -threshold, 2D-clusters them (filters.cluster, min 30 points / 3 m tolerance), drops empty clusters, decimates 5:1, and writes buildings_points.csv in EPSG:4326 with intensity / return / source-class columns.

In-process Go (go-gdal raster only): groups points by ClusterID, merges clusters whose boxes overlap, and per building:
orients the bounding box by the roof-ridge bearing (PCA of the top 30% band, P90 height cut) when the band is line-like (aspect ≥ 1.2); otherwise full-cloud PCA, with near-square buildings (eigenvalue ratio below -snap-to-parcel) snapped to the parcel bearing;
samples DSM − DTM cells inside the box for roof height (90th percentile) and classifies the roof: flat when relief < -flat-relief, otherwise pitched with slope (rise over the ridge width, capped at 60°) and ridge bearing;
votes trees: round convex hull, dark intensity, low last-return fraction, vegetation-class fraction — components with -tree-vote-min votes are dropped.

Writes buildings.geojson (WGS84, closed 5-vertex oriented-box rings) and optionally components.csv.

## Tuning & limitations

Trees fused to a house pull the cluster: raise -threshold (5–6) and/or lower the cluster tolerance in pipeline3.json to de-contaminate.
The Iowa EPT carries no reliable ASPRS source classes, so the vegetation vote (and bld_frac/veg_frac) is usually dead; the dark-intensity and last-return votes carry the tree filtering.
Footprints are oriented boxes, not true outlines; area_m2 is box area and over-estimates L-shaped or hipped footprints.
Height is roof-top height above local ground, not wall height; parapets add ∼0.5–1 m.
SMRF infers ground under roofs, so the DTM there is an interpolation — fine for flat terrain (Iowa), less so for hilly ground.
Memory: PDAL runs with --nostream; a 1 km² parcel at ∼10 pts/m² is ∼10 M points ≈ 1–2 GB RAM.
Other states: swap -ept-json to https://s3-us-west-2.amazonaws.com/usgs-lidar-public/ST_FullState/ept.json.

## Validating results

Load buildings.geojson into QGIS over an aerial basemap and check box
orientation against actual roofs. If polygons fail to load, verify the
GeoJSON coordinates are a list of rings ([[[lon,lat],...]]) — a flat
ring passes json.load but breaks feature readers; converting
(ogr2ogr -f GPKG buildings.geojson check.gpkg) is the reliable check,
since ogrinfo -so only reads metadata.
