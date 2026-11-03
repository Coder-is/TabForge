//go:build linux

package filelock

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the clock tick of /proc/<pid>/stat starttime. It is part of the
// kernel ABI and 100 on every mainstream architecture.
const userHZ = 100

// processStart reports when the process now using pid was created, from its
// start time in ticks since boot plus the boot time in /proc/stat.
func processStart(pid int) (time.Time, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	// The command name may contain spaces and parentheses; fields follow the last ')'.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64) // field 22: starttime
	if err != nil {
		return time.Time{}, false
	}
	system, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(system), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			boot, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(boot, 0).Add(time.Duration(ticks) * time.Second / userHZ), true
		}
	}
	return time.Time{}, false
}
