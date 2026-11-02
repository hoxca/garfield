package cmd

import (
	"os"
	"strconv"
	"strings"
)

// physicalMemory reports the machine's installed RAM in bytes, or 0 when it
// cannot be read. MemTotal is the kernel's view of usable RAM, which is the
// right figure to budget against.
func physicalMemory() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb * 1024
	}
	return 0
}
