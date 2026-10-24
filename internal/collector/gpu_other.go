//go:build !darwin && !linux && !windows

package collector

// readGPUs is a stub for platforms without a GPU collector.
func readGPUs() ([]GPU, error) {
	return nil, nil
}
