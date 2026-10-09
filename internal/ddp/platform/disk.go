package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	giB         = uint64(1 << 30)
	warningPct  = uint64(10)
	criticalPct = uint64(5)
)

// Disk reports capacity for the filesystem containing path.
func Disk(path string) Result {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return unknownDiskResult()
	}
	available, capacity, ok := diskUsage(stat)
	if !ok {
		return unknownDiskResult()
	}
	state, severity := diskState(available, capacity)
	percent := float64(available) * 100 / float64(capacity)
	message := "Filesystem containing project directory has sufficient capacity."
	if state == "failing" {
		message = "Filesystem containing project directory has low capacity."
	}
	return Result{
		State: state, Severity: severity,
		Message: message,
		Value:   fmt.Sprintf("%d bytes available (%.1f%%)", available, percent),
	}
}

func unknownDiskResult() Result {
	return Result{State: "unknown", Severity: "critical", Message: "Filesystem containing project directory capacity is unknown."}
}

func diskUsage(stat unix.Statfs_t) (available, capacity uint64, ok bool) {
	if stat.Bsize <= 0 || stat.Blocks == 0 {
		return 0, 0, false
	}
	blocks, blockSize := uint64(stat.Blocks), uint64(stat.Bsize)
	if blocks > ^uint64(0)/blockSize {
		return 0, 0, false
	}
	availableBlocks := uint64(0)
	if stat.Bavail > 0 {
		availableBlocks = uint64(stat.Bavail)
	}
	if availableBlocks > blocks || availableBlocks > ^uint64(0)/blockSize {
		return 0, 0, false
	}
	return availableBlocks * blockSize, blocks * blockSize, true
}

func diskState(available, capacity uint64) (string, string) {
	if capacity == 0 {
		return "unknown", "critical"
	}
	if available < criticalBytes || belowPercent(available, capacity, criticalPct) {
		return "failing", "critical"
	}
	if available < giB || belowPercent(available, capacity, warningPct) {
		return "failing", "warning"
	}
	return "ok", "warning"
}

const (
	criticalBytes = uint64(256 << 20)
)

func belowPercent(available, capacity, percent uint64) bool {
	// ceil(capacity*percent/100) avoids multiplication overflow at uint64 max.
	quotient, remainder := capacity/100, capacity%100
	threshold := quotient*percent + (remainder*percent+99)/100
	return available < threshold
}
