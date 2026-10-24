//go:build darwin

package collector

import "testing"

const ioregSample = `+-o AGXAcceleratorG13G_B0  <class AGXAcceleratorG13G_B0, id 0x1000006f7, registered, matched, active, busy 0 (3577 ms), retain 84>
    {
      "IOClass" = "AGXAcceleratorG13G_B0"
      "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=3527245824,"Tiler Utilization %"=0,"recoveryCount"=0,"lastRecoveryTime"=0,"Renderer Utilization %"=18,"TiledSceneBytes"=688128,"Device Utilization %"=19,"SplitSceneCount"=0,"Allocated PB Size"=103940096,"In use system memory"=494682112}
      "gpu-core-count" = 8
      "model" = "Apple M1"
    }
`

func TestParseIOAccelerator(t *testing.T) {
	gpus := parseIOAccelerator(ioregSample)
	if len(gpus) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(gpus))
	}

	gpu := gpus[0]
	if gpu.Name != "Apple M1" {
		t.Errorf("unexpected name: %q", gpu.Name)
	}
	if gpu.Vendor != "Apple" {
		t.Errorf("unexpected vendor: %q", gpu.Vendor)
	}
	if gpu.Cores != 8 {
		t.Errorf("unexpected cores: %d", gpu.Cores)
	}
	if gpu.Utilization == nil || *gpu.Utilization != 19 {
		t.Errorf("unexpected utilization: %v", gpu.Utilization)
	}
	if gpu.MemoryUsed == nil || *gpu.MemoryUsed != 494682112 {
		t.Errorf("unexpected memory used: %v", gpu.MemoryUsed)
	}
	if gpu.MemoryTotal != nil {
		t.Errorf("Apple unified memory should not report a total, got %v", *gpu.MemoryTotal)
	}
}

func TestParseIOAcceleratorWithoutGPU(t *testing.T) {
	if gpus := parseIOAccelerator("ioreg output without accelerators"); len(gpus) != 0 {
		t.Fatalf("expected no GPUs, got %+v", gpus)
	}
}
