package platform

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskTempDir(t *testing.T) {
	path := t.TempDir()
	result := Disk(path)
	if result.State != "ok" && result.State != "failing" {
		t.Fatalf("Disk(tempdir) = %#v", result)
	}
	// Other processes can allocate space between calls; validate this observation.
	var available uint64
	var percent float64
	if n, err := fmt.Sscanf(result.Value, "%d bytes available (%f%%)", &available, &percent); err != nil || n != 2 || percent < 0 || percent > 100 {
		t.Fatalf("invalid capacity observation: %+v", result)
	}
	if result.Severity != "warning" && result.Severity != "critical" {
		t.Fatalf("invalid capacity severity: %+v", result)
	}
	if !strings.Contains(result.Message, "Filesystem containing project directory") {
		t.Fatalf("message = %q", result.Message)
	}
	if !strings.Contains(result.Value, "bytes available") || !strings.Contains(result.Value, "%") {
		t.Fatalf("value = %q", result.Value)
	}
}

func TestDiskInvalidPath(t *testing.T) {
	result := Disk("/path/that/does/not/exist/ddp")
	if result.State != "unknown" || result.Severity != "critical" {
		t.Fatalf("Disk(invalid) = %#v", result)
	}
	if strings.Contains(result.Message, "does/not/exist") || !strings.Contains(result.Message, "capacity is unknown") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestDiskUsageValidation(t *testing.T) {
	base := unix.Statfs_t{Blocks: 100, Bsize: 4096, Bavail: 25}
	available, capacity, ok := diskUsage(base)
	if !ok || available != 25*4096 || capacity != 100*4096 {
		t.Fatalf("diskUsage(base) = %d, %d, %t", available, capacity, ok)
	}
	for name, stat := range map[string]unix.Statfs_t{
		"zero blocks":             {Bsize: 4096},
		"zero size":               {Blocks: 1},
		"available exceeds total": {Blocks: 1, Bsize: 4096, Bavail: 2},
		"capacity overflow":       {Blocks: ^uint64(0), Bsize: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := diskUsage(stat); ok {
				t.Fatalf("diskUsage(%s) reported valid", name)
			}
		})
	}
}

func TestDiskStateByteBoundaries(t *testing.T) {
	if state, severity := diskState(criticalBytes-1, 10*giB); state != "failing" || severity != "critical" {
		t.Fatalf("critical byte boundary = %q, %q", state, severity)
	}
	if state, severity := diskState(giB-1, 10*giB); state != "failing" || severity != "warning" {
		t.Fatalf("warning byte boundary = %q, %q", state, severity)
	}
}

func TestDiskStateThresholds(t *testing.T) {
	const capacity = uint64(1 << 40)
	for _, test := range []struct {
		name, wantState, wantSeverity string
		available                     uint64
	}{
		{"critical", "failing", "critical", capacity * 4 / 100},
		{"warning", "failing", "warning", capacity * 9 / 100},
		{"healthy", "ok", "warning", (capacity + 9) / 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, severity := diskState(test.available, capacity)
			if state != test.wantState || severity != test.wantSeverity {
				t.Fatalf("diskState(%d, %d) = %q, %q", test.available, capacity, state, severity)
			}
		})
	}
	if state, _ := diskState(^uint64(0), ^uint64(0)); state != "ok" {
		t.Fatalf("max capacity state = %q", state)
	}
}
