// raster/grid.go
// Package raster wraps go-gdal raster reads for the DSM/DTM grids PDAL
// writes, plus the cell sampling the roof classifier needs.
package raster

import (
	gdal "github.com/usace-cloud-compute/go-gdal"
	"github.com/usace-nsi/entwine-buildings/geometry"
)

// Nodata: grids use -9999; -9999 fails the ">" test, so we test against
// -9998.
const Nodata = -9998.0

// Grid is a single-band raster in EPSG:4326.
type Grid struct {
	Values       []float32
	GeoTransform [6]float64
	Width        int
	Height       int
}

// Read opens a single-band GeoTIFF via go-gdal.
func (g Grid) Read(path string) (Grid, error) {
	ds, err := gdal.Open(path, gdal.ReadOnly)
	if err != nil {
		return g, err
	}
	defer ds.Close()
	g.Width = ds.RasterXSize()
	g.Height = ds.RasterYSize()
	g.GeoTransform = ds.GeoTransform()
	g.Values = make([]float32, g.Width*g.Height)
	ds.RasterBand(1).IO(gdal.Read, 0, 0, g.Width, g.Height, g.Values, g.Width, g.Height, 0, 0)
	return g, nil
}

// HeightCells samples (x, y, heightAboveGround) cells inside the box from
// the DSM/DTM difference, in projected meters. Cells at or below the
// nodata sentinel, or with height above ground < minH, are skipped.
func (g Grid) HeightCells(dtm Grid, box [4][2]float64, minH float64) [][3]float64 {
	var cells [][3]float64
	for py := 0; py < g.Height; py++ {
		for px := 0; px < g.Width; px++ {
			lon := g.GeoTransform[0] + (float64(px)+0.5)*g.GeoTransform[1] + (float64(py)+0.5)*g.GeoTransform[2]
			lat := g.GeoTransform[3] + (float64(px)+0.5)*g.GeoTransform[4] + (float64(py)+0.5)*g.GeoTransform[5]
			x, y := geometry.Wgs84To3857(lon, lat)
			if !geometry.PointInQuad(x, y, box) {
				continue
			}
			i := py*g.Width + px
			if g.Values[i] <= Nodata || dtm.Values[i] <= Nodata {
				continue
			}
			v := float64(g.Values[i] - dtm.Values[i])
			if v < minH {
				continue
			}
			cells = append(cells, [3]float64{x, y, v})
		}
	}
	return cells
}
