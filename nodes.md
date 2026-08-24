# 1) build the dev container (the `go build` inside is the compile+link check)
docker build -t entwine-dev .

# 2) run it. Bind the workspace at the SAME path host & container, mount the
#    Docker socket so the Go binary can drive the host daemon for PDAL.
docker run -it --rm \
  -v "$PWD":"$PWD" -w "$PWD" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  entwine-dev \
  -parcel example/parcel.geojson -workdir "$PWD/out"

# iterate on the Go without rebuilding the image:
docker run -it --rm -v "$PWD":"$PWD" -w "$PWD" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --entrypoint sh entwine-dev -c 'go build -o entwine-buildings . && ./entwine-buildings -parcel example/parcel.geojson -workdir "$PWD/out"'

# test just the Go+GDAL step (needs dsm.tif/dtm.tif/buildings_points.csv present):
#   ... entwine-dev -gdal-only -parcel example/parcel.geojson -workdir "$PWD/out"
# preview the generated pipelines with no daemon:
#   ... entwine-dev -dry-run -parcel example/parcel.geojson -workdir "$PWD/out"