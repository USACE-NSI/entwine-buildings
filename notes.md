# notes

How to build, run, and iterate on `entwine-buildings`.

## Build

Native build. Needs Go (go.mod declares 1.27) plus a GDAL C library on the
build host, because the `go-gdal` dependency is cgo:

    go build -o entwine-buildings .

The repo no longer ships the old dev-container workflow (no Dockerfile).
The only container step is the PDAL image, which the Go binary drives.

## Docker prep (one-time)

    docker pull pdal/pdal
    docker volume create entwine-work

`entwine-work` is a named volume mounted at `/work` inside the container.
Intermediates (`parcel_cloud.laz`, `dsm.tif`, `dtm.tif`,
`buildings_points.csv`, pipeline JSONs) persist there between runs.

## Run

Full pipeline (PDAL stages + Go analysis):

    ./entwine-buildings -parcel example/res-parcel3.geojson \
      -workdir /work -docker-volume entwine-work

The binary shells out to:

    docker run --rm -v entwine-work:/work -w /work pdal/pdal \
      pdal pipeline /work/pipelineN.json --nostream

(falling back to the same command without `--nostream` on older images).
`-workdir` must be the host mount point of the named volume, because the
Go step reads the grids/CSV from there.

## Go-only iteration

Once `dsm.tif`, `dtm.tif`, and `buildings_points.csv` exist in the workdir,
tune Go-side behavior (tree votes, orientation, snap, roof thresholds)
without re-running PDAL:

    ./entwine-buildings -parcel example/res-parcel3.geojson \
      -workdir /work -docker-volume entwine-work \
      -skip-pdal -debug-components

Re-run the full PDAL stages only when you change `-threshold` or
`-resolution` (or the pipeline source in `main.go` — cluster
`tolerance`/`min_points` are hardcoded in the pipeline3 string and the
JSONs are regenerated every run, so hand-editing them won't persist).

## Native Linux alternative (no named volume)

Without `-docker-volume`, the workdir is bind-mounted at the same path
host and container:

    ./entwine-buildings -parcel example/res-parcel3.geojson \
      -workdir /work -docker-workdir /work

Only usable when the host path is directly mountable (native Linux);
on macOS/WSL use the named-volume form.

## Misc

- The pipeline JSONs are rewritten into the workdir on every invocation —
  inspect them there to see exactly what PDAL receives.
- S3 EPT fetches occasionally fail with transient errors; just re-run.
- `-debug-components` writes `components.csv` next to the outputs; use it
  to tune tree votes / orientation before committing to a full re-run.
- Validate `buildings.geojson` with `ogr2ogr -f GPKG` (reads features);
  `ogrinfo -so` only reads metadata and will bless a broken file.
