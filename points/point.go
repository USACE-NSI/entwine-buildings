// points/point.go
// Package points loads the PDAL "building" points CSV
// (writers.text: header line + "X,Y,Z,ClusterID,Intensity,ReturnNumber,
// NumberOfReturns,SourceClass").
package points

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Point is one clustered point. Optional columns are 0 when missing.
type Point struct {
	Lon, Lat, Z float64
	Cluster     int
	Intensity   float64 // 0 if column missing
	RetNum      int     // 0 if column missing
	NumRet      int
	SrcClass    int // original ASPRS class, 0 if column missing
}

// Load reads the clustered-points CSV at path, skipping the header line.
func Load(path string) []Point {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var pts []Point
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		fld := strings.Split(sc.Text(), ",")
		if lineNo == 1 || len(fld) < 3 {
			continue
		}
		x, e1 := strconv.ParseFloat(fld[0], 64)
		y, e2 := strconv.ParseFloat(fld[1], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		z := 0.0
		if len(fld) > 2 {
			z, _ = strconv.ParseFloat(fld[2], 64)
		}
		c := 0
		if len(fld) > 3 {
			c, _ = strconv.Atoi(fld[3])
		}
		var p Point
		p.Lon, p.Lat, p.Z, p.Cluster = x, y, z, c
		if len(fld) > 4 {
			p.Intensity, _ = strconv.ParseFloat(fld[4], 64)
		}
		if len(fld) > 5 {
			p.RetNum, _ = strconv.Atoi(fld[5])
		}
		if len(fld) > 6 {
			p.NumRet, _ = strconv.Atoi(fld[6])
		}
		if len(fld) > 7 {
			p.SrcClass, _ = strconv.Atoi(fld[7])
		}
		pts = append(pts, p)
	}
	return pts
}

// ByCluster groups points by their cluster id.
func ByCluster(pts []Point) map[int][]Point {
	by := map[int][]Point{}
	for _, p := range pts {
		by[p.Cluster] = append(by[p.Cluster], p)
	}
	return by
}
