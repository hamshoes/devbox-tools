package project

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxDepth = 4

// FindRepos returns repo paths relative to root, sorted. A repo is a folder
// containing .git (folder or file); the walk stops at repos, hidden folders,
// node_modules and vendor, and goes at most maxDepth levels deep.
func FindRepos(root string) ([]string, error) {
	var repos []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			return fs.SkipDir
		}
		if strings.Count(rel, string(filepath.Separator)) >= maxDepth {
			return fs.SkipDir
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, rel)
			return fs.SkipDir
		}
		return nil
	})
	sort.Strings(repos)
	return repos, err
}
