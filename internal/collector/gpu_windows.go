//go:build windows

package collector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	windowsGPUOnce   sync.Once
	windowsGPUStatic []GPU
)

func readGPUs() ([]GPU, error) {
	if gpus := readNvidiaGPUs(); len(gpus) > 0 {
		return gpus, nil
	}
	// Vendor utilities for AMD/Intel do not expose utilization in a portable
	// way; fall back to enumerating adapters through WMI.
	windowsGPUOnce.Do(func() {
		windowsGPUStatic = readVideoControllers()
	})
	return windowsGPUStatic, nil
}

func readNvidiaGPUs() []GPU {
	bin := nvidiaSMIPath()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, nvidiaQueryArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(string(out))
}

func nvidiaSMIPath() string {
	for _, name := range []string{"nvidia-smi.exe", "nvidia-smi"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "NVIDIA Corporation", "NVSMI", "nvidia-smi.exe"),
		`C:\Windows\System32\nvidia-smi.exe`,
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func readVideoControllers() []GPU {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		ps = "powershell.exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	script := `[Console]::OutputEncoding=[Text.Encoding]::UTF8; ` +
		`Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name`
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var gpus []GPU
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "MICROSOFT BASIC") || strings.Contains(upper, "MICROSOFT REMOTE") ||
			strings.Contains(upper, "HYPER-V") {
			continue
		}
		seen[key] = struct{}{}
		gpus = append(gpus, GPU{Index: len(gpus), Name: name, Vendor: gpuVendor(name)})
	}
	return gpus
}
