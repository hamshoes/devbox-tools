// Package stats collects VM status from /proc, statfs and tmux.
package stats

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

type CPUStats struct {
	Percent float64    `json:"percent"`
	Cores   int        `json:"cores"`
	Load    [3]float64 `json:"load"`
	History []CPUPoint `json:"history"`
}

type MemStats struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Percent   float64 `json:"percent"`
}

type DiskStats struct {
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

type HomeStats struct {
	Size       uint64    `json:"size"`
	Top        []DirSize `json:"top"`
	MeasuredAt time.Time `json:"measured_at"`
	Took       string    `json:"took"`
	Measuring  bool      `json:"measuring"`
}

// Snapshot is the JSON served by /api/stats.
type Snapshot struct {
	Version  string    `json:"version"`
	Hostname string    `json:"hostname"`
	Now      time.Time `json:"now"`
	BootTime time.Time `json:"boot_time"`
	Running  string    `json:"running"`
	CPU      CPUStats  `json:"cpu"`
	Memory   MemStats  `json:"memory"`
	Disk     DiskStats `json:"disk"`
	Home     HomeStats `json:"home"`
	Clients  []Client  `json:"clients"`
}

// Config sets what a Collector measures and how often.
type Config struct {
	Version      string
	Home         string
	CPUInterval  time.Duration
	CPUHistory   int
	WalkInterval time.Duration
}

// Collector owns the background samplers and serves snapshots of their state.
type Collector struct {
	cfg Config
	wg  sync.WaitGroup

	mu      sync.RWMutex
	cpu     *ring
	cpuLast float64
	cores   int
	home    HomeStats
}

func New(cfg Config) *Collector {
	return &Collector{cfg: cfg, cpu: newRing(cfg.CPUHistory), home: HomeStats{Top: []DirSize{}, Measuring: true}}
}

// Start launches the samplers; they stop when ctx is cancelled.
func (c *Collector) Start(ctx context.Context) {
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.sampleCPU(ctx) }()
	go func() { defer c.wg.Done(); c.measureHome(ctx) }()
}

// Wait blocks until the samplers have stopped.
func (c *Collector) Wait() { c.wg.Wait() }

func (c *Collector) sampleCPU(ctx context.Context) {
	idle, total, cores, err := readCPU()
	if err != nil {
		slog.Error("cpu sampler stopped", "err", err)
		return
	}
	c.mu.Lock()
	c.cores = cores
	c.mu.Unlock()
	t := time.NewTicker(c.cfg.CPUInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			i, tot, _, err := readCPU()
			if err != nil || tot <= total {
				continue
			}
			pct := (1 - float64(i-idle)/float64(tot-total)) * 100
			idle, total = i, tot
			c.mu.Lock()
			c.cpuLast = pct
			c.cpu.add(CPUPoint{Time: now, Percent: pct})
			c.mu.Unlock()
		}
	}
}

// measureHome walks the home folder at startup and then every WalkInterval.
// It is a single loop, so walks never overlap.
func (c *Collector) measureHome(ctx context.Context) {
	t := time.NewTicker(c.cfg.WalkInterval)
	defer t.Stop()
	for {
		start := time.Now()
		size, top, err := walk(ctx, c.cfg.Home, 10)
		if err == nil {
			c.mu.Lock()
			c.home = HomeStats{Size: size, Top: top, MeasuredAt: time.Now(), Took: time.Since(start).Round(time.Millisecond).String()}
			c.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Snapshot returns the current state. It never triggers a directory walk.
func (c *Collector) Snapshot(ctx context.Context) Snapshot {
	now := time.Now()
	s := Snapshot{Version: c.cfg.Version, Now: now}
	s.Hostname, _ = os.Hostname()
	if up, err := readUptime(); err == nil {
		s.BootTime = now.Add(-up)
		s.Running = formatDuration(up)
	}
	if total, avail, err := readMem(); err == nil && total > 0 {
		s.Memory = MemStats{Total: total, Used: total - avail, Available: avail, Percent: float64(total-avail) / float64(total) * 100}
	}
	if total, used, free, err := statfs(c.cfg.Home); err == nil {
		s.Disk = DiskStats{Total: total, Used: used, Free: free}
		if used+free > 0 {
			s.Disk.Percent = float64(used) / float64(used+free) * 100
		}
	}
	s.CPU.Load = readLoad()
	c.mu.RLock()
	s.CPU.Percent, s.CPU.Cores, s.CPU.History = c.cpuLast, c.cores, c.cpu.points()
	s.Home = c.home
	c.mu.RUnlock()
	var err error
	if s.Clients, err = listClients(ctx); err != nil || s.Clients == nil {
		s.Clients = []Client{}
	}
	return s
}
