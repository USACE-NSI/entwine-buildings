// pdal/config.go
// Package pdal generates the three PDAL pipeline files (EPT -> parcel
// cloud, grids, clustered building points) and runs them via docker.
package pdal

import (
	"fmt"
	"math"

	"github.com/usace-nsi/entwine-buildings/parcel"
)

// Config carries everything the pipeline JSON needs.
type Config struct {
	Rings        parcel.Rings
	EptJSON      string
	DockerWD     string
	Threshold    float64
	Resolution   float64
	GridBounds   [4]float64 // minLon, minLat, maxLon, maxLat in EPSG:4326
	SkipPDAL     bool
	PdalImage    string
	DockerVolume string
}

// MainRing is the ring with the largest bounding-box area; it drives the
// EPT fetch and the parcel-axis snap.
func (c Config) MainRing() parcel.Ring {
	mainRing, maxA := c.Rings[0], 0.0
	for _, r := range c.Rings {
		if a := r.BboxArea(); a > maxA {
			maxA, mainRing = a, r
		}
	}
	return mainRing
}

// PolyWKT is the EPSG:3857 WKT of the main ring, for readers.ept.
func (c Config) PolyWKT() string { return c.MainRing().WKT() }

// BoundsString formats the grid bounds (parcel bbox + ~20 m margin) as the
// "([xmin, xmax], [ymin, ymax])" string writers.gdal expects.
func (c Config) BoundsString() string {
	minLon, minLat, maxLon, maxLat := c.GridBounds[0], c.GridBounds[1], c.GridBounds[2], c.GridBounds[3]
	latMid := (minLat + maxLat) / 2
	mLat := 20.0 / 111320.0
	mLon := 20.0 / (111320.0 * math.Cos(latMid*math.Pi/180))
	return fmt.Sprintf("([%.8f, %.8f], [%.8f, %.8f])",
		minLon-mLon, maxLon+mLon, // [xmin, xmax]
		minLat-mLat, maxLat+mLat) // [ymin, ymax]
}
