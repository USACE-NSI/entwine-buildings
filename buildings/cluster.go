// buildings/cluster.go
// Package buildings turns clustered point groups into oriented building
// footprints with roof/height/tree-vote attributes, and writes the
// GeoJSON/CSV outputs.
package buildings

import (
	"github.com/usace-nsi/entwine-buildings/geometry"
	"github.com/usace-nsi/entwine-buildings/points"
)

// Cluster is one PDAL cluster of building points.
type Cluster struct {
	ID     int
	Points []points.Point
	LL     [][2]float64 // projected meters
}

// Clusters is a set of clusters.
type Clusters []Cluster

// FromPoints groups points by cluster id, dropping clusters with fewer
// than minPoints points, and projects each to EPSG:3857.
func FromPoints(pts []points.Point, minPoints int) Clusters {
	byCID := points.ByCluster(pts)
	var clusters Clusters
	for cid, cpts := range byCID {
		if len(cpts) < minPoints {
			continue
		}
		ll := make([][2]float64, len(cpts))
		for j, p := range cpts {
			x, y := geometry.Wgs84To3857(p.Lon, p.Lat)
			ll[j] = [2]float64{x, y}
		}
		clusters = append(clusters, Cluster{ID: cid, Points: cpts, LL: ll})
	}
	return clusters
}

// Group is a set of merged clusters (one roof split into two clusters).
type Group struct {
	LL     [][2]float64
	Points []points.Point
}

// MergeOverlapping merges clusters whose boxes overlap (one roof split
// into two clusters) and returns the groups.
func (cs Clusters) MergeOverlapping() []Group {
	boxes := make([]geometry.Box2, len(cs))
	for i := range cs {
		ang, _, _, _ := geometry.PcaAngle(cs[i].LL)
		boxes[i] = geometry.BoxAt(cs[i].LL, ang)
	}
	groups := make([]Group, 0, len(cs))
	used := make([]bool, len(cs))
	for i := 0; i < len(cs); i++ {
		if used[i] {
			continue
		}
		g := Group{
			LL:     append([][2]float64(nil), cs[i].LL...),
			Points: append([]points.Point(nil), cs[i].Points...),
		}
		for j := i + 1; j < len(cs); j++ {
			if used[j] || boxes[i].OverlapFrac(boxes[j]) <= 0.25 {
				continue
			}
			used[j] = true
			g.LL = append(g.LL, cs[j].LL...)
			g.Points = append(g.Points, cs[j].Points...)
		}
		used[i] = true
		groups = append(groups, g)
	}
	return groups
}
