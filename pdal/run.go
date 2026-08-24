// pdal/run.go
package pdal

import (
	"os"
	"os/exec"
)

// RunPipeline runs one PDAL pipeline inside docker. Tries --nostream first,
// falling back to no flag for older PDAL. Mounts either a named volume or a
// bind mount of workdir.
func RunPipeline(image, volume, dockerWD, pipelineFile string) error {
	// The workdir path *inside* the container. Default to /work when unset
	// so the mount destination is never empty (an empty destination is an
	// invalid mount: "destination can't be '/'").
	if dockerWD == "" {
		dockerWD = "/work"
	}
	wd := dockerWD
	if wd[0] != '/' {
		wd = "/" + wd
	}

	attempts := [][]string{
		{"pdal", "pipeline", wd + "/" + pipelineFile, "--nostream"},
		{"pdal", "pipeline", wd + "/" + pipelineFile}, // fallback for older PDAL without --nostream
	}
	// Named volume -> mount it at the container workdir. No volume ->
	// bind-mount the host workdir at the same path (native Linux).
	mount := wd
	if volume != "" {
		mount = volume + ":" + wd // "entwine-work" + ":" + "/work" = "entwine-work:/work"
	}
	var lastErr error
	for _, a := range attempts {
		args := append([]string{"run", "--rm", "-v", mount, "-w", wd, image}, a...)
		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		lastErr = cmd.Run()
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}
