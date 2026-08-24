// flags.go
package main

import "flag"

// Flags holds every tunable of the run. Populated by flag.Parse() through
// the pointers in initFlags; consumed as a value by run.
type Flags struct {
	Parcel           string
	Workdir          string
	EptJSON          string
	Threshold        float64
	MinPoints        int
	Resolution       float64
	PdalImage        string
	SkipPDAL         bool
	DockerVolume     string
	DockerWorkdir    string
	DropTrees        bool
	TreeVoteMin      int
	HullRound        float64
	TreeIntensityPct float64
	TreeLastRet      float64
	FlatRelief       float64
	SnapToParcel     float64
	OrientRidge      bool
	DebugComponents  bool
	InventoryDir     string
}

var flags Flags

func init() {
	flag.StringVar(&flags.Parcel, "parcel", "", "parcel GeoJSON (WGS84), required")
	flag.StringVar(&flags.Workdir, "workdir", ".", "scratch dir for intermediate files")
	flag.StringVar(&flags.EptJSON, "ept-json",
		"https://s3-us-west-2.amazonaws.com/usgs-lidar-public/IA_FullState/ept.json",
		"EPT ept.json URL")
	flag.Float64Var(&flags.Threshold, "threshold", 4.0, "min height above ground (m) for cluster points")
	flag.IntVar(&flags.MinPoints, "min-points", 3, "min cluster points per building (after decimation)")
	flag.Float64Var(&flags.Resolution, "resolution", 0.00001, "grid resolution in degrees (~1 m at 42N)")
	flag.StringVar(&flags.PdalImage, "pdal-image", "pdal/pdal", "PDAL docker image")
	flag.BoolVar(&flags.SkipPDAL, "skip-pdal", false, "skip PDAL docker steps (reuse existing outputs)")
	flag.StringVar(&flags.DockerVolume, "docker-volume", "",
		"named docker volume for PDAL scratch files (e.g. entwine-work). When set, -workdir must be the volume's mount point (e.g. /work). Empty = bind-mount -workdir at the same path (native Linux).")
	flag.StringVar(&flags.DockerWorkdir, "docker-workdir", "",
		"workdir path inside the PDAL container (default: /work when -docker-volume is set, else -workdir).")
	flag.BoolVar(&flags.DropTrees, "drop-trees", true, "drop components that vote as tree canopy")
	flag.IntVar(&flags.TreeVoteMin, "tree-vote-min", 2, "tree votes required to drop a component; 0 disables tree filtering")
	flag.Float64Var(&flags.HullRound, "hull-round", 0.85, "convex-hull/box area ratio < this earns a 'round' tree vote")
	flag.Float64Var(&flags.TreeIntensityPct, "tree-intensity-pct", 25,
		"dark-intensity cut as a percentile of building mean intensities (0 disables the dark vote)")
	flag.Float64Var(&flags.TreeLastRet, "tree-lastret", 0.35, "last-return fraction < this earns a 'penetrating canopy' vote")
	flag.Float64Var(&flags.FlatRelief, "flat-relief", 0.75, "roof relief (m) below which the roof is classified flat")
	flag.Float64Var(&flags.SnapToParcel, "snap-to-parcel", 1.25,
		"if a building's PCA eigenvalue ratio is below this, snap its box to the parcel's long axis (0 disables)")
	flag.BoolVar(&flags.OrientRidge, "orient-ridge", true,
		"orient boxes by roof-ridge bearing (top band of the cluster point cloud); falls back to full-cloud PCA when the ridge signal is weak. Disable with -orient-ridge=false")
	flag.BoolVar(&flags.DebugComponents, "debug-components", false, "write components.csv with per-building features for threshold tuning")
	flag.StringVar(&flags.InventoryDir, "inventory-dir", "", "cache dir for the EPT resource inventory; auto-selects -ept when set and -ept is empty")
}
