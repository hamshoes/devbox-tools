package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const maxParseDepth = 3

// word is one shell word; lit is false when it can't be known statically ($x, $(…), globs of braces…).
type word struct {
	s   string
	lit bool
}

// push is a `git push` found in a command. unparsed means we only suspect one.
type push struct {
	dir      string // from git -C, relative to the command's working directory
	args     []word // everything after "push"
	unparsed bool
}

func toWord(w *syntax.Word) word {
	var b strings.Builder
	for _, p := range w.Parts {
		if !appendLit(&b, p) {
			return word{}
		}
	}
	return word{b.String(), true}
}

func appendLit(b *strings.Builder, part syntax.WordPart) bool {
	switch p := part.(type) {
	case *syntax.Lit:
		v := p.Value
		for i := 0; i < len(v); i++ { // g\it is git
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			b.WriteByte(v[i])
		}
	case *syntax.SglQuoted:
		b.WriteString(p.Value)
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			if !appendLit(b, q) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

var wordSplit = regexp.MustCompile(`[A-Za-z0-9_]+`)

// looksLikePush is the fallback for text we can't parse: both words appear, separately.
func looksLikePush(s string) bool {
	var git, push bool
	for _, w := range wordSplit.FindAllString(s, -1) {
		git, push = git || w == "git", push || w == "push"
	}
	return git && push
}

// findPushes returns every git push in a shell command: in lists, pipes, subshells, command
// substitutions and `sh -c` strings. If the command can't be parsed, it reports a suspected one when the
// text mentions both words.
func findPushes(src string, depth int) []push {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		if looksLikePush(src) {
			return []push{{unparsed: true}}
		}
		return nil
	}
	var found []push
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		words := make([]word, len(call.Args))
		for i, a := range call.Args {
			words[i] = toWord(a)
		}
		found = append(found, classify(words, depth)...)
		return true
	})
	return found
}

func classify(words []word, depth int) []push {
	args := unwrap(words)
	if len(args) == 0 {
		return nil
	}
	if !args[0].lit { // $g push: we can't tell what runs
		for _, a := range args[1:] {
			if a.lit && a.s == "push" {
				return []push{{unparsed: true}}
			}
		}
		return nil
	}
	switch filepath.Base(args[0].s) {
	case "git":
		if p, ok := gitPush(args[1:]); ok {
			return []push{p}
		}
	case "bash", "sh", "zsh", "dash", "ksh":
		if script, ok := shellScript(args[1:]); ok {
			return nested(script, depth)
		}
	case "eval":
		var parts []string
		for _, a := range args[1:] {
			if !a.lit {
				return nil
			}
			parts = append(parts, a.s)
		}
		return nested(strings.Join(parts, " "), depth)
	}
	return nil
}

func nested(script string, depth int) []push {
	if depth >= maxParseDepth {
		if looksLikePush(script) {
			return []push{{unparsed: true}}
		}
		return nil
	}
	return findPushes(script, depth+1)
}

// shellScript is the string after -c (possibly in a bundle such as -lc).
func shellScript(args []word) (string, bool) {
	for i, a := range args {
		if !a.lit || !strings.HasPrefix(a.s, "-") || strings.HasPrefix(a.s, "--") {
			continue
		}
		if strings.Contains(a.s, "c") {
			for _, s := range args[i+1:] {
				if s.lit && !strings.HasPrefix(s.s, "-") {
					return s.s, true
				}
			}
		}
	}
	return "", false
}

// takesValue are the options of each wrapper whose value is the next word.
var wrapperValue = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true, "-R": true, "-T": true, "-U": true, "--user": true, "--group": true, "--host": true, "--prompt": true, "--chdir": true},
	"env":     {"-u": true, "-C": true, "-S": true, "--unset": true, "--chdir": true, "--split-string": true},
	"exec":    {"-a": true},
	"nice":    {"-n": true, "--adjustment": true},
	"time":    {"-f": true, "-o": true, "--format": true, "--output": true},
	"command": {},
	"nohup":   {},
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// unwrap drops leading VAR=value words and the wrappers sudo, env, command, exec, nohup, time and nice.
func unwrap(words []word) []word {
	for len(words) > 0 {
		w := words[0]
		if w.lit && assignment.MatchString(w.s) {
			words = words[1:]
			continue
		}
		name := filepath.Base(w.s)
		vals, ok := wrapperValue[name]
		if !w.lit || !ok {
			return words
		}
		rest := words[1:]
		for len(rest) > 0 && rest[0].lit {
			o := rest[0].s
			switch {
			case o == "--":
				rest = rest[1:]
				goto next
			case assignment.MatchString(o):
				rest = rest[1:]
			case strings.HasPrefix(o, "-") && o != "-":
				if name == "command" && (strings.Contains(o, "v") || strings.Contains(o, "V")) {
					return nil // a lookup, not a run
				}
				rest = rest[1:]
				if vals[o] && len(rest) > 0 {
					rest = rest[1:]
				}
			default:
				goto next
			}
		}
	next:
		words = rest
	}
	return words
}

var gitValueOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--exec-path": true, "--config-env": true, "--super-prefix": true}
var gitFlagOpts = map[string]bool{"--no-pager": true, "-p": true, "-P": true, "--paginate": true, "--bare": true, "--no-replace-objects": true,
	"--literal-pathspecs": true, "--glob-pathspecs": true, "--noglob-pathspecs": true, "--icase-pathspecs": true, "--no-optional-locks": true}

// gitPush reports whether args (after "git") run `push`, skipping git's global options.
func gitPush(args []word) (push, bool) {
	var p push
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.lit {
			return p, false
		}
		switch {
		case a.s == "-C" && i+1 < len(args):
			i++
			if args[i].lit {
				p.dir = filepath.Join(p.dir, args[i].s)
				if filepath.IsAbs(args[i].s) {
					p.dir = args[i].s
				}
			}
		case gitValueOpts[a.s]:
			i++
		case gitFlagOpts[a.s], strings.HasPrefix(a.s, "--") && gitValueOpts[a.s[:strings.IndexByte(a.s+"=", '=')]]:
		case strings.HasPrefix(a.s, "-"):
			return p, false // an option we don't know: don't guess where the subcommand is
		default:
			if a.s == "push" {
				p.args = args[i+1:]
				return p, true
			}
			return p, false
		}
	}
	return p, false
}

// ---- the ask ----

type pushArgs struct {
	remote   string
	refspecs []string
	force    bool
	del      bool
}

var pushValueOpts = map[string]bool{"--repo": true, "-o": true, "--push-option": true, "--receive-pack": true, "--exec": true}

func parsePushArgs(args []word) (pa pushArgs) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.lit {
			continue
		}
		s := a.s
		switch {
		case s == "--":
		case pushValueOpts[s]:
			i++
		case s == "--force" || strings.HasPrefix(s, "--force-with-lease") || s == "--force-if-includes":
			pa.force = true
		case s == "--delete":
			pa.del = true
		case strings.HasPrefix(s, "--"):
		case strings.HasPrefix(s, "-") && len(s) > 1:
			pa.force = pa.force || strings.Contains(s, "f")
			pa.del = pa.del || strings.Contains(s, "d")
		case pa.remote == "":
			pa.remote = s
		default:
			pa.refspecs = append(pa.refspecs, s)
			pa.force = pa.force || strings.HasPrefix(s, "+")
			pa.del = pa.del || strings.HasPrefix(s, ":")
		}
	}
	return pa
}

// git runs one read-only git command in dir with a 2s limit. A failure is "", false.
func git(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-pager"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

// buildPushAsk describes a push for the approver. Every git lookup is best effort.
func buildPushAsk(command, cwd string, p push) (title, detail, dir string) {
	dir = cwd
	if p.dir != "" {
		dir = p.dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
	}
	if p.unparsed {
		return "git push (unparsed command)", "Could not tell what this command pushes.\n\n" + capRunes(command, 500), dir
	}
	pa := parsePushArgs(p.args)

	repo := filepath.Base(dir)
	if top, ok := git(dir, "rev-parse", "--show-toplevel"); ok && top != "" {
		repo = filepath.Base(top)
	}
	branch, _ := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if n := len(pa.refspecs); n > 0 {
		last := strings.TrimPrefix(pa.refspecs[n-1], "+")
		if _, dst, ok := strings.Cut(last, ":"); ok && dst != "" {
			last = dst
		} else if ok {
			last = branch // "src:" with nothing after it
		}
		if last != "HEAD" {
			branch = strings.TrimPrefix(last, "refs/heads/")
		}
	}
	remote := pa.remote
	if remote == "" {
		remote, _ = git(dir, "config", "--get", "branch."+branch+".remote")
	}
	if remote == "" {
		remote = "(default remote)"
	}
	title = fmt.Sprintf("git push: %s %s → %s", repo, branch, remote)

	lines := []string{capRunes(command, 500)}
	if pa.force {
		lines = append(lines, "FORCE PUSH")
	}
	if pa.del {
		lines = append(lines, "DELETES A REMOTE BRANCH")
	}
	if rng := pushRange(dir, remote); rng != "" {
		if n, ok := git(dir, "rev-list", "--count", rng); ok {
			lines = append(lines, n+" commit(s) ahead of "+strings.TrimSuffix(rng, "..HEAD"))
		}
		if stat, ok := git(dir, "diff", "--shortstat", rng); ok && stat != "" {
			lines = append(lines, stat)
		}
		if subj, ok := git(dir, "log", "--format=%s", "-5", rng); ok && subj != "" {
			lines = append(lines, "Latest commits:")
			for _, s := range strings.Split(subj, "\n") {
				lines = append(lines, "  "+capRunes(s, 100))
			}
		}
	}
	return title, strings.Join(lines, "\n"), dir
}

// pushRange is <upstream>..HEAD, or <remote>/<default branch>..HEAD when there is no upstream.
func pushRange(dir, remote string) string {
	if up, ok := git(dir, "rev-parse", "--abbrev-ref", "@{u}"); ok && up != "" {
		return up + "..HEAD"
	}
	if head, ok := git(dir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); ok && head != "" {
		return head + "..HEAD"
	}
	for _, b := range []string{"main", "master"} {
		if _, ok := git(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+b); ok {
			return remote + "/" + b + "..HEAD"
		}
	}
	return ""
}

func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
