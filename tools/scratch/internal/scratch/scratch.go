// Package scratch implements `proj scratch`, its `ls` and its `clean`.
package scratch

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const helloMain = `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`

// New creates a dated scratch folder under root and returns its path.
func New(root string, args []string) (string, error) {
	name := "tmp"
	var goInit bool
	var seenName bool
	for _, a := range args {
		switch {
		case a == "--go":
			goInit = true
		case strings.HasPrefix(a, "--"):
			return "", fmt.Errorf("unknown flag %s", a)
		case !seenName:
			name, seenName = a, true
		default:
			return "", errors.New("usage: proj scratch [name] [--go]")
		}
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", errors.New("scratch name must not contain path separators")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	base := time.Now().Format("20060102") + "-" + name
	dir := filepath.Join(root, base)
	for n := 2; ; n++ {
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		dir = filepath.Join(root, base+"-"+strconv.Itoa(n))
	}
	if goInit {
		cmd := exec.Command("go", "mod", "init", "scratch/"+name)
		cmd.Dir = dir
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(helloMain), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

type entry struct {
	name string
	mod  time.Time
	size int64
}

func entries(root string) ([]entry, error) {
	des, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, d := range des {
		if !d.IsDir() {
			continue // also skips symlinks, which have type ModeSymlink
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		var size int64
		filepath.WalkDir(filepath.Join(root, d.Name()), func(_ string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				if i, err := e.Info(); err == nil {
					size += i.Size()
				}
			}
			return nil
		})
		out = append(out, entry{d.Name(), info.ModTime(), size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	return out, nil
}

// List prints scratch folders, newest first, with size and age.
func List(root string) error {
	es, err := entries(root)
	if err != nil {
		return err
	}
	for _, e := range es {
		fmt.Printf("%-40s %8s  %s\n", e.name, human(e.size), age(time.Since(e.mod)))
	}
	return nil
}

// Clean deletes scratch folders older than days after confirming on /dev/tty.
func Clean(root string, args []string) error {
	days := 30
	if len(args) > 1 {
		return errors.New("usage: proj scratch clean [days]")
	}
	if len(args) == 1 {
		d, err := strconv.Atoi(args[0])
		if err != nil || d < 0 {
			return fmt.Errorf("invalid days %q", args[0])
		}
		days = d
	}
	es, err := entries(root)
	if err != nil {
		return err
	}
	var old []entry
	for _, e := range es {
		if time.Since(e.mod) >= time.Duration(days)*24*time.Hour {
			old = append(old, e)
		}
	}
	if len(old) == 0 {
		fmt.Printf("nothing older than %d days\n", days)
		return nil
	}
	for _, e := range old {
		fmt.Printf("%-40s %8s  %s\n", e.name, human(e.size), age(time.Since(e.mod)))
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open /dev/tty: %w", err)
	}
	defer tty.Close()
	fmt.Fprintf(tty, "delete %d folder(s) from %s? [y/N] ", len(old), root)
	answer, _ := bufio.NewReader(tty).ReadString('\n')
	if strings.TrimSpace(answer) != "y" {
		fmt.Println("aborted")
		return nil
	}
	for _, e := range old {
		if err := os.RemoveAll(filepath.Join(root, e.name)); err != nil {
			return err
		}
	}
	fmt.Printf("deleted %d folder(s)\n", len(old))
	return nil
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGT"[exp])
}

func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
