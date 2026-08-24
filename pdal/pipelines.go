// pdal/pipelines.go
package pdal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Pipelines are the three PDAL pipeline JSON documents.
type Pipelines struct {
	one   string
	two   string
	three string
}

// Pipelines builds the three pipeline JSON documents from the config.
func (c Config) Pipelines() Pipelines {
	resS := strconv.FormatFloat(c.Resolution, 'f', 8, 64)
	radS := strconv.FormatFloat(c.Resolution*4, 'f', 8, 64)
	thrS := strconv.FormatFloat(c.Threshold, 'f', 3, 64)
	boundsS := c.BoundsString()
	wd := c.DockerWD

	one := fmt.Sprintf(`[
  {"type":"readers.ept","filename":"%s","polygon":"%s","requests":8},
  {"type":"filters.reprojection","out_srs":"EPSG:26915"},
  {"type":"filters.expression","expression":"Classification != 7 && Classification != 12 && Classification != 18"},
  {"type":"filters.assign","value":"SourceClass = Classification"},
  {"type":"filters.smrf","cell":2,"slope":0.25,"window":21},
  {"type":"filters.hag_nn","count":8,"max_distance":10,"allow_extrapolation":false,"class":2},
  {"type":"filters.assign","value":"DTM = Z - HeightAboveGround"},
  {"type":"writers.las","filename":"%s/parcel_cloud.laz","extra_dims":["DTM=Double","SourceClass=Double"]}
]`, c.EptJSON, c.PolyWKT(), wd)

	two := fmt.Sprintf(`[
  {"type":"readers.las","filename":"%s/parcel_cloud.laz"},
  {"type":"filters.reprojection","out_srs":"EPSG:4326"},
  {"type":"writers.gdal","filename":"%s/dsm.tif","dimension":"Z","output_type":"max","binmode":true,"resolution":%s,"bounds":"%s","gdaldriver":"GTiff","data_type":"Float32","nodata":-9999,"window_size":1},
  {"type":"writers.gdal","filename":"%s/dtm.tif","dimension":"DTM","output_type":"idw","radius":%s,"power":2,"resolution":%s,"bounds":"%s","gdaldriver":"GTiff","data_type":"Float32","nodata":-9999,"window_size":1}
]`, wd, wd, resS, boundsS, wd, radS, resS, boundsS)

	three := fmt.Sprintf(`[
  {"type":"readers.las","filename":"%s/parcel_cloud.laz"},
  {"type":"filters.expression","expression":"Z-DTM > %s"},
  {"type":"filters.cluster","min_points":30,"tolerance":3},
  {"type":"filters.expression","expression":"ClusterID > 0"},
  {"type":"filters.decimation","step":5},
  {"type":"filters.reprojection","out_srs":"EPSG:4326"},
  {"type":"writers.text","filename":"%s/buildings_points.csv","order":"X:8,Y:8,Z:2,ClusterID:0,Intensity:0,ReturnNumber:0,NumberOfReturns:0,SourceClass:0","keep_unspecified":"false"}
]`, wd, thrS, wd)

	return Pipelines{one: one, two: two, three: three}
}

// Write writes the three pipeline files into the workdir.
func (p Pipelines) Write(workdir string) error {
	for name, content := range map[string]string{
		"pipeline1.json": p.one,
		"pipeline2.json": p.two,
		"pipeline3.json": p.three,
	} {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(content), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// RunAll runs the three pipelines in order via docker, skipping when
// -skip-pdal is set.
func (p Pipelines) RunAll(image, volume, dockerWD string, skip bool) error {
	if skip {
		return nil
	}
	for i, name := range []string{"pipeline1.json", "pipeline2.json", "pipeline3.json"} {
		if err := RunPipeline(image, volume, dockerWD, name); err != nil {
			return fmt.Errorf("pdal %s: %w", name, err)
		}
		fmt.Printf("== %s done\n", name)
		_ = i
	}
	return nil
}
