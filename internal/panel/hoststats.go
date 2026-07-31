package panel

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostStats samples the panel host's CPU utilisation from /proc/stat. Inside a
// container /proc reports the host's values, which is what the overview shows.
type hostStats struct {
	mu         sync.RWMutex
	cpuPercent float64
	prevIdle   uint64
	prevTotal  uint64
}

func (h *hostStats) run(ctx context.Context) {
	h.sample() // prime the first delta
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.sample()
		}
	}
}

func (h *hostStats) sample() {
	idle, total, ok := readCPU()
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.prevTotal != 0 && total > h.prevTotal {
		dt := total - h.prevTotal
		di := idle - h.prevIdle
		if dt > 0 {
			h.cpuPercent = 100 * float64(dt-di) / float64(dt)
		}
	}
	h.prevIdle = idle
	h.prevTotal = total
}

// CPUPercent returns the most recent host CPU utilisation (0–100).
func (h *hostStats) CPUPercent() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cpuPercent < 0 {
		return 0
	}
	return h.cpuPercent
}

// readCPU returns idle (idle+iowait) and total jiffies from /proc/stat.
func readCPU() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, s := range fields[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return idle, total, true
}

// hostMem returns used/total bytes and used percent from /proc/meminfo.
func hostMem() (used, total uint64, percent float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	var memTotal, memAvail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			memTotal, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			memAvail, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	memTotal *= 1024
	memAvail *= 1024
	if memAvail > memTotal {
		memAvail = memTotal
	}
	used = memTotal - memAvail
	if memTotal > 0 {
		percent = 100 * float64(used) / float64(memTotal)
	}
	return used, memTotal, percent
}
