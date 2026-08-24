# entwine-buildings

Extracts building footprints (geometry + `height_m` + `area_m2` + `area_sqft`)
from the USGS 3DEP Entwine Point Tile (EPT) LiDAR archive for a parcel
polygon, using PDAL + GDAL in Docker.

## Requirements
- Docker
- Go 1.21+ (only to build the runner; stdlib only, no dependencies)

## Quickstart
    cd entwine-buildings
    go build -o entwine-buildings .
    ./entwine-buildings -parcel example/parcel.geojson -workdir ./work

Outputs (in the workdir):
| file | what |
|---|---|
| `buildings.geojson` | **final**: footprints in `-out-srs` with height_m, area_m2, area_sqft, points, hag_p10/p90, method |
| `parcel_cloud.laz` | full parcel point cloud in NAD83 UTM with `HeightAboveGround` |
| `buildings.laz` | building-classified points only (trees excluded), `ClusterID` per building |
| `dsm.tif` / `dtm.tif` | 0.5 m IDW surfaces (all returns / ground only) |
| `buildings_points.csv` | decimated building points (ClusterID, X, Y, HAG) |
| `pipeline1.json`, `pipeline2.json`, `params.json`, `build_footprints.py` | generated, inspectable |
| `summary.json` | drop counts at each filter stage |

## Flags (all have sensible defaults)
| flag | default | meaning |
|---|---|---|
| `-parcel` | — | parcel GeoJSON, **WGS84 lon/lat** (Polygon/MultiPolygon/Feature/FeatureCollection) |
| `-ept` | IA_FullState ept.json | any USGS 3DEP state EPT URL |
| `-workdir` | ./work | output directory |
| `-out-srs` | EPSG:4326 | output GeoJSON SRS |
| `-resolution` | 0.5 | DSM/DTM cell size (m) |
| `-building-hag-min` | 4.0 | min height-above-ground (m) to be a building point |
| `-cluster-tolerance` / `-cluster-min-points` | 3.0 / 30 | 2D building cluster link distance / min size |
| `-min-building-area` | 100 | drop footprints smaller than this (m²) — removes small trees, cars |
| `-max-roof-variability` | 3.0 | drop components whose roof height spread p90−p10 exceeds this (m) — removes domed tree canopies |
| `-smrf-window` / `-smrf-slope` / `-smrf-cell` / `-smrf-threshold` | 33 / 0.15 / 1.0 / 0.5 | SMRF ground filter tuning |
| `-requests` | 15 | concurrent EPT HTTP requests |
| `-dry-run` | false | write pipelines/scripts only, no docker |

## How it works
1. **Go runner** reprojects the parcel to EPSG:3857 (the EPT's storage CRS,
   per its `ept.json` `"reprojection"` block) with closed-form Web Mercator,
   picks the NAD83 UTM zone from the parcel longitude, and generates the
   pipelines.
2. **PDAL step 1** queries the EPT by bbox + parcel polygon, reprojects to
   UTM, drops noise (ASPRS classes 7/18/12), wipes unreliable classifications,
   runs SMRF ground + HAG, and writes the cloud plus a DSM (all returns) and
   DTM (ground only) with `writers.gdal` (IDW, `window_size=6`).
3. **PDAL step 2** classifies buildings: points with HAG > threshold are
   2D-clustered (`filters.cluster`); clusters below the min size are dropped.
4. **GDAL step** (osgeo/gdal image, Python): `dsm − dtm > threshold`, clipped
   to the parcel, connected components, small/domed blobs dropped, and each
   surviving component must contain building-cluster points. Components are
   vectorized with `gdal.Polygonize`, simplified, reprojected, and exported
   as GeoJSON. Height = 85th percentile of HAG over the matched cluster
   points (roof height above ground); area via GEOS in the UTM CRS.

## Tuning & limitations
- Dense urban areas: raise `-building-hag-min` (5–6) and
  `-cluster-min-points`; sparse rural: lower both.
- Touching buildings merge into one footprint (connected-component limit).
- Height is roof-top height above local ground, not wall height; parapets
  add ~0.5–1 m.
- SMRF infers ground *under* roofs, so DTM there is an interpolation — fine
  for flat terrain (Iowa), less so for hilly ground.
- Memory: PDAL runs in standard mode (`--nostream`); a 1 km² parcel at
  ~10 pts/m² ≈ 10M points ≈ 1–2 GB RAM.
- Other states: swap `-ept` to
  `https://s3-us-west-2.amazonaws.com/usgs-lidar-public/<STATE>_FullState/ept.json`.