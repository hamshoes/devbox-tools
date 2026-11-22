// Package platform reads the devbox platform constants. The devbox repo owns
// their values (internal/platform/platform.env, installed in the image as
// /etc/devbox/platform.env); this package holds the names only.
//
// A value is taken from the process environment first, then from the file.
// Every name must be set; Load fails with one error naming all that are missing.
package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// File is where the image installs the platform constants.
const File = "/etc/devbox/platform.env"

// Names of the platform constants.
const (
	EnvSSHPort        = "BOX_SSH_PORT"
	EnvControlPort    = "BOX_CONTROL_PORT"
	EnvPairPortMin    = "BOX_PAIR_PORT_MIN"
	EnvPairPortMax    = "BOX_PAIR_PORT_MAX"
	EnvWebPort        = "BOX_WEB_PORT"
	EnvReservedPorts  = "BOX_RESERVED_PORTS"
	EnvMaxForwards    = "BOX_MAX_FORWARDS"
	EnvPairPorts      = "BOX_PAIR_PORTS"
	EnvUser           = "BOX_USER"
	EnvWorkGroup      = "BOX_WORK_GROUP"
	EnvWorkGID        = "BOX_WORK_GID"
	EnvNotifySpool    = "BOX_NOTIFY_SPOOL" // e.g. /var/lib/box-notify; the value comes from the environment
	EnvLockFile       = "BOX_LOCK_FILE"
	EnvConfigDir      = "BOX_CONFIG_DIR"
	EnvDevtoolsDir    = "BOX_DEVTOOLS_DIR"
	EnvStateDir       = "BOX_STATE_DIR"
	EnvTetherStateDir = "BOX_TETHER_STATE_DIR"
	EnvControldSocket = "BOX_CONTROLD_SOCKET"
)

// Names lists every platform constant, in contract order.
var Names = []string{
	EnvSSHPort, EnvControlPort, EnvPairPortMin, EnvPairPortMax, EnvWebPort, EnvReservedPorts,
	EnvMaxForwards, EnvPairPorts, EnvUser, EnvWorkGroup, EnvWorkGID, EnvNotifySpool, EnvLockFile,
	EnvConfigDir, EnvDevtoolsDir, EnvStateDir, EnvTetherStateDir, EnvControldSocket,
}

// PortRange is an inclusive range of TCP ports; a single port has Lo == Hi.
type PortRange struct{ Lo, Hi int }

// Contains reports whether p is in the range.
func (r PortRange) Contains(p int) bool { return p >= r.Lo && p <= r.Hi }

func (r PortRange) String() string {
	if r.Lo == r.Hi {
		return strconv.Itoa(r.Lo)
	}
	return fmt.Sprintf("%d-%d", r.Lo, r.Hi)
}

// Config holds the platform constants. Paths named relative to $HOME in the
// contract are absolute here.
type Config struct {
	SSHPort     int
	ControlPort int
	PairPortMin int
	PairPortMax int
	WebPort     int
	Reserved    []PortRange
	MaxForwards int
	PairPorts   map[string]int // platform pairs' fixed VM ports
	User        string
	WorkGroup   string
	WorkGID     int
	NotifySpool string
	LockFile    string

	ConfigDir      string
	DevtoolsDir    string
	StateDir       string
	TetherStateDir string
	ControldSocket string
}

// IsReserved reports whether p is a platform port that is never forwarded.
func (c *Config) IsReserved(p int) bool {
	return slices.ContainsFunc(c.Reserved, func(r PortRange) bool { return r.Contains(p) })
}

// ReservedString is the reserved list as the contract writes it, e.g. "22 4100-4199 8080".
func (c *Config) ReservedString() string {
	parts := make([]string, len(c.Reserved))
	for i, r := range c.Reserved {
		parts[i] = r.String()
	}
	return strings.Join(parts, " ")
}

// Load reads the constants from the environment, then from File.
func Load() (*Config, error) {
	return load(os.LookupEnv, File)
}

func load(lookup func(string) (string, bool), file string) (*Config, error) {
	fileVals, ferr := readFile(file)
	if ferr != nil && !errors.Is(ferr, fs.ErrNotExist) {
		return nil, fmt.Errorf("platform: %w", ferr)
	}
	vals := map[string]string{}
	var missing []string
	for _, n := range Names {
		if v, ok := lookup(n); ok && v != "" {
			vals[n] = v
		} else if v, ok := fileVals[n]; ok && v != "" {
			vals[n] = v
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		where := file
		if ferr != nil {
			where += " (not found)"
		}
		return nil, fmt.Errorf("platform: %s not set in the environment or %s", strings.Join(missing, ", "), where)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("platform: %w", err)
	}
	p := &parser{vals: vals}
	c := &Config{
		SSHPort:        p.port(EnvSSHPort),
		ControlPort:    p.port(EnvControlPort),
		PairPortMin:    p.port(EnvPairPortMin),
		PairPortMax:    p.port(EnvPairPortMax),
		WebPort:        p.port(EnvWebPort),
		Reserved:       p.ranges(EnvReservedPorts),
		MaxForwards:    p.positive(EnvMaxForwards),
		PairPorts:      p.pairPorts(EnvPairPorts),
		User:           vals[EnvUser],
		WorkGroup:      vals[EnvWorkGroup],
		WorkGID:        p.positive(EnvWorkGID),
		NotifySpool:    p.abs(EnvNotifySpool),
		LockFile:       p.abs(EnvLockFile),
		ConfigDir:      p.underHome(home, EnvConfigDir),
		DevtoolsDir:    p.underHome(home, EnvDevtoolsDir),
		StateDir:       p.underHome(home, EnvStateDir),
		TetherStateDir: p.underHome(home, EnvTetherStateDir),
		ControldSocket: p.underHome(home, EnvControldSocket),
	}
	if c.PairPortMin > c.PairPortMax {
		p.fail(EnvPairPortMin, "greater than "+EnvPairPortMax)
	}
	if len(p.errs) > 0 {
		return nil, fmt.Errorf("platform: %s", strings.Join(p.errs, "; "))
	}
	return c, nil
}

// readFile parses KEY=value lines of a shell env file: blank lines, comments,
// an "export " prefix, single or double quotes and trailing comments are allowed.
func readFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: not KEY=value", path, n)
		}
		if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
			end := strings.IndexByte(v[1:], v[0])
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unterminated quote", path, n)
			}
			v = v[1 : end+1]
		} else if i := strings.IndexAny(v, " \t#"); i >= 0 {
			v = v[:i]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}

type parser struct {
	vals map[string]string
	errs []string
}

func (p *parser) fail(name, why string) {
	p.errs = append(p.errs, fmt.Sprintf("%s=%q: %s", name, p.vals[name], why))
}

func (p *parser) positive(name string) int {
	n, err := strconv.Atoi(p.vals[name])
	if err != nil || n <= 0 {
		p.fail(name, "want a positive integer")
		return 0
	}
	return n
}

func (p *parser) port(name string) int {
	n := p.positive(name)
	if n > 65535 {
		p.fail(name, "want a port 1-65535")
	}
	return n
}

func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && n <= 65535
}

func (p *parser) ranges(name string) []PortRange {
	var out []PortRange
	for f := range strings.FieldsSeq(p.vals[name]) {
		lo, hi, isRange := strings.Cut(f, "-")
		if !isRange {
			hi = lo
		}
		l, ok1 := parsePort(lo)
		h, ok2 := parsePort(hi)
		if !ok1 || !ok2 || l > h {
			p.fail(name, "want space-separated ports or LO-HI ranges")
			return nil
		}
		out = append(out, PortRange{l, h})
	}
	return out
}

func (p *parser) pairPorts(name string) map[string]int {
	out := map[string]int{}
	for f := range strings.FieldsSeq(p.vals[name]) {
		pair, port, ok := strings.Cut(f, "=")
		n, okPort := parsePort(port)
		if !ok || pair == "" || !okPort {
			p.fail(name, "want space-separated name=port")
			return nil
		}
		out[pair] = n
	}
	return out
}

func (p *parser) abs(name string) string {
	v := p.vals[name]
	if !filepath.IsAbs(v) {
		p.fail(name, "want an absolute path")
	}
	return filepath.Clean(v)
}

func (p *parser) underHome(home, name string) string {
	v := p.vals[name]
	if filepath.IsAbs(v) || !filepath.IsLocal(v) {
		p.fail(name, "want a path relative to $HOME")
	}
	return filepath.Join(home, v)
}
