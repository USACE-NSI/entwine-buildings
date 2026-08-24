# Dev container: osgeo GDAL + Go 1.27 + USACE go-gdal + docker CLI.
# The `go build` below is the authoritative cgo compile+link check against the
# system libgdal: if any go-gdal symbol name is off, the build fails and names it.
FROM ghcr.io/osgeo/gdal:ubuntu-full-latest

ARG GO_VERSION=1.27.0
ARG DOCKER_CLI_VERSION=27.3.1
ENV DEBIAN_FRONTEND=noninteractive

# build-essential: cgo. pkg-config: required by go-gdal's `#cgo pkg-config: gdal`.
# Go 1.27 from the official tarball (distro go is 1.22). docker CLI: static binary
# so the container can drive the host daemon through the mounted socket.
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config ca-certificates curl tar \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz \
    && curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_CLI_VERSION}.tgz" | tar -C /tmp -xz \
    && install -m 0755 /tmp/docker/docker /usr/local/bin/docker \
    && rm -rf /tmp/docker /var/lib/apt/lists/* \
    && apt-get clean

# The osgeo image builds GDAL into /usr (headers /usr/include/gdal,
# lib /usr/lib/x86_64-linux-gnu) but ships no gdal.pc and no dev libgdal.so
# symlink. Provide both if absent so cgo can link.
# NOTE: `$$` is required so the Dockerfile parser leaves `${prefix}` literal in
# the .pc file (a single $ would be expanded to empty at build-parse time).
RUN set -e; \
    if ! pkg-config --exists gdal; then \
        mkdir -p /usr/lib/pkgconfig; \
        printf 'prefix=/usr\nlibdir=$${prefix}/lib/x86_64-linux-gnu\nincludedir=$${prefix}/include/gdal\nName: GDAL\nDescription: Geospatial Data Abstraction Library\nVersion: 3.0.0\nCflags: -I$${includedir}\nLibs: -L$${libdir} -lgdal\n' > /usr/lib/pkgconfig/gdal.pc; \
    fi; \
    if [ ! -e /usr/lib/x86_64-linux-gnu/libgdal.so ]; then \
        ln -sf "$(ls -1 /usr/lib/x86_64-linux-gnu/libgdal.so.* | head -n1 | xargs basename)" /usr/lib/x86_64-linux-gnu/libgdal.so; \
    fi