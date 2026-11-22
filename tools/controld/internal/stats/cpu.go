package stats

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CPUPoint is one CPU usage sample.
type CPUPoint struct {
	Time    time.Time `json:"time"`
	Percent float64   `json:"percent"`
}

// ring is a fixed-size buffer that overwrites its oldest sample.
type ring struct {
	buf  []CPUPoint
	next int
	full bool
}

func newRing(n int) *ring {
	if n < 1 {
		n = 1
	}
	return &ring{buf: make([]CPUPoint, n)}
}

func (r *ring) add(p CPUPoint) {
	r.buf[r.next] = p
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
}

// points returns the samples oldest first.
func (r *ring) points() []CPUPoint {
	if !r.full {
		return append([]CPUPoint{}, r.buf[:r.next]...)
	}
	return append(append([]CPUPoint{}, r.buf[r.next:]...), r.buf[:r.next]...)
}

// readCPU returns the aggregate idle (idle+iowait) and total jiffies, and the core count.
func readCPU() (idle, total uint64, cores int, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cores++
			continue
		}
		// user nice system idle iowait irq softirq steal; guest time is already in user.
		for i := 1; i < len(fields) && i <= 8; i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return 0, 0, 0, err
			}
			total += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, err
	}
	if total == 0 {
		return 0, 0, 0, fmt.Errorf("no cpu line in /proc/stat")
	}
	return idle, total, cores, nil
}

func readLoad() (l [3]float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return l
	}
	f := strings.Fields(string(b))
	for i := 0; i < 3 && i < len(f); i++ {
		l[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return l
}
