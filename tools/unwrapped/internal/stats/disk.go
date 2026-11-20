package stats

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// DirSize is the on-disk size of one top-level entry under the home folder.
type DirSize struct {
	Name string `json:"name"`
	Size uint64 `json:"size"`
}

func statfs(path string) (total, used, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, st.Bavail * bs, nil
}

type inode struct{ dev, ino uint64 }

// walk sums the space files under root use on disk (like du), without following
// symlinks, and returns the topN largest top-level entries.
func walk(ctx context.Context, root string, topN int) (uint64, []DirSize, error) {
	var total uint64
	sizes := map[string]uint64{}
	seen := map[inode]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil || d == nil {
			return nil // skip anything that errors
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Nlink > 1 && !d.IsDir() {
			k := inode{uint64(st.Dev), uint64(st.Ino)}
			if seen[k] {
				return nil
			}
			seen[k] = true
		}
		n := uint64(st.Blocks) * 512
		total += n
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." {
			name, _, _ := strings.Cut(rel, string(filepath.Separator))
			sizes[name] += n
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	top := make([]DirSize, 0, len(sizes))
	for name, size := range sizes {
		top = append(top, DirSize{name, size})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Size != top[j].Size {
			return top[i].Size > top[j].Size
		}
		return top[i].Name < top[j].Name
	})
	if len(top) > topN {
		top = top[:topN]
	}
	return total, top, nil
}
