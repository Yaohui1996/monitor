//go:build linux

package collector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func readGPUs() ([]GPU, error) {
	if gpus := readNvidiaGPUs(); len(gpus) > 0 {
		return gpus, nil
	}
	return readDRMGPUs(), nil
}

func readNvidiaGPUs() []GPU {
	bin := nvidiaSMIPath()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, nvidiaQueryArgs...).Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(string(out))
}

func nvidiaSMIPath() string {
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p
	}
	for _, p := range []string{
		"/usr/bin/nvidia-smi",
		"/usr/local/bin/nvidia-smi",
		"/usr/local/nvidia/bin/nvidia-smi",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// readDRMGPUs covers GPUs exposed through the DRM sysfs interface (AMD and
// Intel in particular). Utilization and VRAM counters exist on amdgpu; other
// drivers fall back to name/vendor only.
func readDRMGPUs() []GPU {
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	var gpus []GPU
	seen := make(map[string]struct{}, len(cards))

	for _, card := range cards {
		if !isDRMCard(filepath.Base(card)) {
			continue
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(card, "device"))
		if err != nil {
			continue
		}
		if _, ok := seen[dev]; ok {
			continue
		}
		seen[dev] = struct{}{}

		vendorID := readSmallFile(filepath.Join(dev, "vendor"))
		vendor := pciVendorName(vendorID)
		name := readSmallFile(filepath.Join(dev, "product_name"))
		if name == "" {
			name = vendor + " GPU"
		}

		g := GPU{Index: len(gpus), Name: strings.TrimSpace(name), Vendor: vendor}
		if v, ok := parseUint(readSmallFile(filepath.Join(dev, "gpu_busy_percent"))); ok {
			g.Utilization = f64ptr(clamp(float64(v), 0, 100))
		}
		if v, ok := parseUint(readSmallFile(filepath.Join(dev, "mem_info_vram_used"))); ok {
			g.MemoryUsed = u64ptr(v)
		}
		if v, ok := parseUint(readSmallFile(filepath.Join(dev, "mem_info_vram_total"))); ok && v > 0 {
			g.MemoryTotal = u64ptr(v)
		}
		if v, ok := readHwmon(dev, "temp1_input"); ok && v > 0 {
			g.Temperature = f64ptr(float64(v) / 1000)
		}
		if v, ok := readHwmon(dev, "power1_average"); ok && v > 0 {
			g.Power = f64ptr(float64(v) / 1e6)
		} else if v, ok := readHwmon(dev, "power1_input"); ok && v > 0 {
			g.Power = f64ptr(float64(v) / 1e6)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func isDRMCard(name string) bool {
	rest := strings.TrimPrefix(name, "card")
	if rest == "" || rest == name {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pciVendorName(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "0x1002":
		return "AMD"
	case "0x10de":
		return "NVIDIA"
	case "0x8086":
		return "Intel"
	case "0x13b5":
		return "ARM"
	case "0x1af4":
		return "Virtio"
	}
	return ""
}

func readHwmon(dev, file string) (uint64, bool) {
	paths, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*", file))
	for _, p := range paths {
		if v, ok := parseUint(readSmallFile(p)); ok {
			return v, true
		}
	}
	return 0, false
}
