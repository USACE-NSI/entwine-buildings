// main.go
// Command entwine-buildings extracts a USGS Entwine (EPT) point cloud for a
// parcel polygon, derives building footprints, heights, roof type and
// square footage, and writes a GeoJSON.
//
// This package is intentionally thin: it evaluates the flags and ships the
// work off to the module components — parcel, pdal, raster, points,
// buildings, and inventory — returning errors up so main owns the fatal
// logging and exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/usace-nsi/entwine-buildings/buildings"
	"github.com/usace-nsi/entwine-buildings/inventory"
	"github.com/usace-nsi/entwine-buildings/parcel"
	"github.com/usace-nsi/entwine-buildings/pdal"
	"github.com/usace-nsi/entwine-buildings/points"
	"github.com/usace-nsi/entwine-buildings/raster"
)

func main() {
	flag.Parse()
	if err := run(flags); err != nil {
		fatal("%v", err)
	}
}

func run(f Flags) error {
	wd, err := filepath.Abs(f.Workdir)
	if err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	if err := os.MkdirAll(wd, 0755); err != nil {
		return fmt.Errorf("workdir: %w", err)
	}
	dockerWD := f.DockerWorkdir
	if dockerWD == "" {
		if f.DockerVolume != "" {
			dockerWD = "/work"
		} else {
			dockerWD = wd
		}
	} // <-- the brace that was missing: dockerWD now lives in run()'s scope

	rings, err := parcel.LoadRings(f.Parcel)
	if err != nil {
		return fmt.Errorf("parcel: %w", err)
	}

	// Auto-select the EPT resource from the inventory cache when a cache
	// dir is set and -ept-json was not explicitly provided.
	eptSet := false
	flag.Visit(func(fl *flag.Flag) {
		if fl.Name == "ept-json" {
			eptSet = true
		}
	})
	if f.InventoryDir != "" && !eptSet {
		inv, res, rerr := inventory.Refresh(context.Background(), f.InventoryDir, inventory.Client{})
		if rerr != nil {
			// A network hiccup must not block a run when a snapshot exists.
			cached, oerr := inventory.Open(f.InventoryDir)
			if oerr != nil {
				return fmt.Errorf("inventory: %w", rerr)
			}
			fmt.Printf("== inventory refresh failed (%v); using cached snapshot\n", rerr)
			inv = cached
		} else if res.Changed {
			fmt.Printf("== inventory updated: +%d -%d resources\n", len(res.Added), len(res.Removed))
		}
		invRings := make([]inventory.Ring, len(rings))
		for i, r := range rings {
			invRings[i] = r // parcel.Ring (defined) -> inventory.Ring (alias of [][2]float64): assignable
		}
		sel := inv.SelectRings(invRings...)
		if !sel.Matched {
			return fmt.Errorf("no EPT resource covers this parcel; use census-only placement")
		}
		f.EptJSON = sel.Resource.URL
		fmt.Printf("== selected %s (utm zone %d, %d pts)\n   %s\n",
			sel.Resource.Name, sel.Zone, sel.Resource.Count, sel.Resource.URL)
	}

	// Grid bounds: the parcel bbox, as the [4]float64 pdal.Config expects.
	minLon, minLat, maxLon, maxLat := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
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

	cfg := pdal.Config{
		Rings:        rings,
		EptJSON:      f.EptJSON,
		DockerWD:     dockerWD,
		Threshold:    f.Threshold,
		Resolution:   f.Resolution,
		GridBounds:   [4]float64{minLon, minLat, maxLon, maxLat},
		SkipPDAL:     f.SkipPDAL,
		PdalImage:    f.PdalImage,
		DockerVolume: f.DockerVolume,
	}
	pipes := cfg.Pipelines()
	if err := pipes.Write(wd); err != nil {
		return fmt.Errorf("writing PDAL pipeline files: %w", err)
	}
	if !f.SkipPDAL {
		if err := pipes.RunAll(f.PdalImage, f.DockerVolume, f.DockerWorkdir, f.SkipPDAL); err != nil {
			return fmt.Errorf("pdal pipelines: %w", err)
		}
	}

	dsm, err := raster.Grid{}.Read(filepath.Join(wd, "dsm.tif"))
	if err != nil {
		return fmt.Errorf("reading dsm.tif: %w", err)
	}
	dtm, err := raster.Grid{}.Read(filepath.Join(wd, "dtm.tif"))
	if err != nil {
		return fmt.Errorf("reading dtm.tif: %w", err)
	}
	fmt.Printf("== grids: %dx%d px, resolution %.8f deg\n", dsm.Width, dsm.Height, dsm.GeoTransform[1])

	bpts := points.Load(filepath.Join(wd, "buildings_points.csv"))
	fmt.Printf("== %d cluster points\n", len(bpts))

	clusters := buildings.FromPoints(bpts, f.MinPoints)
	groups := clusters.MergeOverlapping()
	if len(groups) < len(clusters) {
		fmt.Printf("== merged %d overlapping clusters\n", len(clusters)-len(groups))
	}

	blds := buildings.NewFromGroups(groups, buildings.Options{
		Grid:         dsm,
		GridDTM:      dtm,
		OrientRidge:  f.OrientRidge,
		SnapToParcel: f.SnapToParcel,
		FlatRelief:   f.FlatRelief,
	})

	dropped := 0
	if f.DropTrees {
		dropped = blds.Filter(f.TreeVoteMin, f.HullRound, f.TreeLastRet, f.TreeIntensityPct)
		if dropped > 0 {
			fmt.Printf("== dropped %d tree-like building(s)\n", dropped)
		}
	}

	if f.DebugComponents {
		if err := blds.WriteDebugCSV(filepath.Join(wd, "components.csv")); err != nil {
			return fmt.Errorf("writing components.csv: %w", err)
		}
		fmt.Printf("== wrote %d building rows to %s\n", len(blds), filepath.Join(wd, "components.csv"))
	}

	outPath := filepath.Join(wd, "buildings.geojson")
	n, err := blds.WriteGeoJSON(outPath)
	if err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	fmt.Printf("== wrote %d building(s) to %s\n", n, outPath)
	return nil
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "entwine-buildings: "+format+"\n", a...)
	os.Exit(1)
}
