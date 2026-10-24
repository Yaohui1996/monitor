package collector

import "testing"

func TestParseNvidiaSMI(t *testing.T) {
	out := "0, NVIDIA GeForce RTX 4090, 37, 2048, 24564, 55, 120.50, 32\n" +
		"1, NVIDIA GeForce RTX 4090, [N/A], [N/A], 24564, [Not Supported], [N/A], 30\n"

	gpus := parseNvidiaSMI(out)
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(gpus))
	}

	first := gpus[0]
	if first.Name != "NVIDIA GeForce RTX 4090" || first.Vendor != "NVIDIA" {
		t.Fatalf("unexpected name/vendor: %+v", first)
	}
	if first.Utilization == nil || *first.Utilization != 37 {
		t.Fatalf("unexpected utilization: %v", first.Utilization)
	}
	if first.MemoryUsed == nil || *first.MemoryUsed != 2048*1024*1024 {
		t.Fatalf("unexpected memory: %v", first.MemoryUsed)
	}
	if first.Temperature == nil || *first.Temperature != 55 {
		t.Fatalf("unexpected temperature: %v", first.Temperature)
	}
	if first.Power == nil || *first.Power != 120.5 {
		t.Fatalf("unexpected power: %v", first.Power)
	}
	if first.FanSpeed == nil || *first.FanSpeed != 32 {
		t.Fatalf("unexpected fan speed: %v", first.FanSpeed)
	}

	second := gpus[1]
	if second.Utilization != nil {
		t.Fatalf("expected nil utilization for N/A, got %v", *second.Utilization)
	}
	if second.Temperature != nil || second.Power != nil {
		t.Fatalf("expected nil unsupported metrics, got %+v", second)
	}
}

func TestParseMetric(t *testing.T) {
	cases := map[string]bool{
		"37":              true,
		" 12.5 ":          true,
		"[N/A]":           false,
		"[Not Supported]": false,
		"":                false,
		"abc":             false,
	}
	for raw, want := range cases {
		if _, ok := parseMetric(raw); ok != want {
			t.Errorf("parseMetric(%q) ok = %v, want %v", raw, ok, want)
		}
	}
}
