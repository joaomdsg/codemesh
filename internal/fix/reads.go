package fix

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/joaomdsg/codemesh/internal/gitx"
)

// Read is a stretch of a module file a step read: lines From to To, 1-based
// and inclusive. From 0 is the whole file; To 0 runs to its end.
type Read struct {
	File     string // relative to the module
	From, To int
}

var (
	segments  = regexp.MustCompile(`[|;&\n]`)
	sedScript = regexp.MustCompile(`^(\d+)(?:,(\d+|\$))?p$`)
	grepLine  = regexp.MustCompile(`^(\d+)[:-]`)
)

// reads lists the module files a shell command names, with the lines a
// `sed -n`, `head` or `tail` in the same pipeline segment shows of them. It
// reads arguments, not the shell: a path built at run time is missed.
func (t *tree) reads(cmd string) []Read {
	if t == nil {
		return nil
	}
	var out []Read
	for _, seg := range segments.Split(cmd, -1) {
		words := strings.Fields(seg)
		for i, w := range words {
			words[i] = strings.Trim(w, `'"`)
		}
		for _, f := range t.files(seg) {
			r := Read{File: f}
			r.From, r.To = shown(words, filepath.Join(t.mod, f))
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// files lists the module files named in a command.
func (t *tree) files(cmd string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(cmd, func(r rune) bool { return strings.ContainsRune(" \t\n;|&<>()'\"`=,", r) }) {
		if rel, ok := t.moduleFile(f); ok && !slices.Contains(out, rel) {
			out = append(out, rel)
		}
	}
	return out
}

// moduleFile resolves a path as the command saw it, from the module, to a
// regular module file.
func (t *tree) moduleFile(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.mod, p)
	}
	rel, ok := inside(t.mod, p)
	if !ok {
		return "", false
	}
	info, err := os.Stat(p)
	return rel, err == nil && info.Mode().IsRegular()
}

// shown is the line range a printing command shows of file: 0, 0 when it is
// the whole file or the command is not one it knows.
func shown(words []string, file string) (from, to int) {
	switch filepath.Base(words[0]) {
	case "sed":
		return sedRange(words)
	case "head":
		if n, _ := count(words); n > 0 {
			return 1, n
		}
	case "tail":
		return tailRange(words, file)
	}
	return 0, 0
}

// sedRange reads `sed -n` with a script such as 5p, 5,9p or 5,$p.
func sedRange(words []string) (from, to int) {
	if !slices.Contains(words, "-n") {
		return 0, 0
	}
	for _, w := range words[1:] {
		if m := sedScript.FindStringSubmatch(w); m != nil {
			from, _ = strconv.Atoi(m[1])
			switch m[2] {
			case "":
				return from, from
			case "$":
				return from, 0
			}
			to, _ = strconv.Atoi(m[2])
			return from, to
		}
	}
	return 0, 0
}

func tailRange(words []string, file string) (from, to int) {
	n, plus := count(words)
	switch {
	case plus:
		return n, 0
	case n > 0:
		total := lineCount(file)
		return max(1, total-n+1), total
	}
	return 0, 0
}

// count reads a head or tail line count: -n N, -nN, -N or --lines=N, and
// whether it was written +N, from line N on.
func count(words []string) (n int, plus bool) {
	for i, w := range words[1:] {
		var v string
		switch {
		case w == "-n" && i+2 < len(words):
			v = words[i+2]
		case strings.HasPrefix(w, "--lines="):
			v = strings.TrimPrefix(w, "--lines=")
		case strings.HasPrefix(w, "-n"):
			v = w[2:]
		case strings.HasPrefix(w, "-"):
			v = w[1:]
		default:
			continue
		}
		plus = strings.HasPrefix(v, "+")
		if n, err := strconv.Atoi(strings.TrimPrefix(v, "+")); err == nil {
			return n, plus
		}
	}
	return 0, false
}

func lineCount(file string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	return gitx.Lines(b)
}

// matched reads a `grep -n` or `rg -n` result as the lines it showed of each
// module file, neighbours merged. Without -n a leading number may be text,
// so it gives nil, as it does for any other command.
func (t *tree) matched(cmd, output string) []Read {
	words := strings.Fields(cmd)
	if t == nil || len(words) == 0 || !numbered(words) {
		return nil
	}
	only := ""
	if fs := t.files(cmd); len(fs) == 1 {
		only = fs[0]
	}
	lines := map[string][]int{}
	var order []string
	for _, l := range strings.Split(output, "\n") {
		f, n, ok := t.grepHit(l, only)
		if !ok {
			continue
		}
		if lines[f] == nil {
			order = append(order, f)
		}
		lines[f] = append(lines[f], n)
	}
	var out []Read
	for _, f := range order {
		out = append(out, spans(f, lines[f])...)
	}
	return out
}

// grepHit reads one result line as a module file and line number. Several
// files print as path:line:text, context lines with - for :; one file named
// alone prints line:text. A path may hold - and digits itself, so each
// separator is tried until the text before it is a module file.
func (t *tree) grepHit(l, only string) (string, int, bool) {
	for i, c := range l {
		if c != ':' && c != '-' || i == 0 {
			continue
		}
		m := grepLine.FindStringSubmatch(l[i+1:])
		if m == nil {
			continue
		}
		if f, ok := t.moduleFile(l[:i]); ok {
			n, _ := strconv.Atoi(m[1])
			return f, n, true
		}
	}
	if m := grepLine.FindStringSubmatch(l); m != nil && only != "" {
		n, _ := strconv.Atoi(m[1])
		return only, n, true
	}
	return "", 0, false
}

// numbered reports whether a grep or rg command prints line numbers.
func numbered(words []string) bool {
	if b := filepath.Base(words[0]); b != "grep" && b != "rg" {
		return false
	}
	return slices.ContainsFunc(words[1:], func(w string) bool {
		return w == "--line-number" || len(w) > 1 && w[0] == '-' && w[1] != '-' && strings.ContainsRune(w, 'n')
	})
}

// spans merges line numbers into runs of consecutive lines.
func spans(file string, ns []int) []Read {
	slices.Sort(ns)
	var out []Read
	for _, n := range slices.Compact(ns) {
		if k := len(out) - 1; k >= 0 && out[k].To+1 == n {
			out[k].To = n
			continue
		}
		out = append(out, Read{File: file, From: n, To: n})
	}
	return out
}
