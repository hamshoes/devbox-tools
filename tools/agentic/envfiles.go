package main

import (
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"
)

// envName is the C5 rule, applied to a file's base name: env, .env, env.example, .env_local, ...
var envName = regexp.MustCompile(`^\.?env([._-].*)?$`)

// isEnvFile reports whether a repo path looks like an env file, examples included:
// .env, .env.*, *.env, *.env.* and (^|/)\.?env([._-].*)?$.
func isEnvFile(p string) bool {
	b := path.Base(p)
	return envName.MatchString(b) || strings.HasSuffix(b, ".env") || strings.Contains(b, ".env.")
}

// pushedEnvFiles lists the env-like files that the commits a push sends add,
// copy, modify or rename, commit by commit: a file added and later deleted is
// still in the pushed history. The commits are those reachable from what is
// pushed and not on its upstream; with no upstream, not on any remote.
func pushedEnvFiles(dir string, p push) ([]string, error) {
	if p.unparsed {
		return nil, errors.New("could not tell what this command pushes")
	}
	revs := pushedRevs(parsePushArgs(p.args), p.args)
	if len(revs) == 0 {
		return nil, nil // deletes only: no commits go out
	}
	args := append([]string{"log", "--name-only", "--diff-filter=ACMR", "--format="}, revs...)
	up, hasUp := "", false
	if len(revs) == 1 && !strings.HasPrefix(revs[0], "-") {
		up, hasUp = git(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", revs[0]+"@{u}")
	}
	if hasUp && up != "" {
		args = append(args, "--not", up)
	} else {
		args = append(args, "--not", "--remotes")
	}
	args = append(args, "--")
	out, ok := git(dir, args...)
	if !ok {
		return nil, errors.New("git log failed")
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" && isEnvFile(f) && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files, nil
}

// pushedRevs is what a push sends, as git log revisions: --all and --mirror
// send every branch, --tags every tag, refspecs their source side, and a bare
// push the current branch.
func pushedRevs(pa pushArgs, args []word) []string {
	var revs []string
	for _, a := range args {
		switch {
		case !a.lit:
		case a.s == "--all" || a.s == "--branches" || a.s == "--mirror":
			revs = append(revs, "--branches")
		case a.s == "--tags":
			revs = append(revs, "--tags")
		}
	}
	for _, r := range pa.refspecs {
		src, _, _ := strings.Cut(strings.TrimPrefix(r, "+"), ":")
		if src != "" {
			revs = append(revs, src)
		}
	}
	if len(revs) == 0 && len(pa.refspecs) == 0 {
		revs = []string{"HEAD"}
	}
	return revs
}
