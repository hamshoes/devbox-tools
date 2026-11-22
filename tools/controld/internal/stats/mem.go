package stats

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readMem returns MemTotal and MemAvailable in bytes.
func readMem() (total, avail uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var gotTotal, gotAvail bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var dst *uint64
		switch fields[0] {
		case "MemTotal:":
			dst, gotTotal = &total, true
		case "MemAvailable:":
			dst, gotAvail = &avail, true
		default:
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		*dst = kb * 1024
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !gotTotal || !gotAvail {
		return 0, 0, fmt.Errorf("MemTotal/MemAvailable missing from /proc/meminfo")
	}
	return total, avail, nil
}
