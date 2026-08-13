// Command scrubfixture turns a real ~/.claude (or ~/.codex) session file or
// directory into a committable fixture: it scrubs sensitive text out of
// every JSONL line while preserving structure, then re-scans its own
// output and fails loudly if anything sensitive remains.
//
// It never writes under -src. Run via `just scrub-fixture <flags>`.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/testing/logfixture/scrub"
)

type mapFlags []scrub.Rule

func (m *mapFlags) String() string {
	var parts []string
	for _, r := range *m {
		parts = append(parts, r.Old+"="+r.New)
	}
	return strings.Join(parts, ",")
}

func (m *mapFlags) Set(s string) error {
	old, new_, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("-map value %q must be OLD=NEW", s)
	}
	*m = append(*m, scrub.Rule{Old: old, New: new_})
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("scrubfixture", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		src    string
		dst    string
		branch string
		dryRun bool
		force  bool
		maps   mapFlags
	)
	fs.StringVar(&src, "src", "", "real session file or directory (read-only)")
	fs.StringVar(&dst, "dst", "", "destination directory")
	fs.Var(&maps, "map", "repeatable literal replacement OLD=NEW, applied to line bytes and (encoded) to output paths")
	fs.StringVar(&branch, "branch", "main", `gitBranch rewrite value (empty string keeps the original)`)
	fs.BoolVar(&dryRun, "dry-run", false, "report only, write nothing")
	fs.BoolVar(&force, "force", false, "write even if findings remain")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if src == "" || dst == "" {
		fmt.Fprintln(stderr, "scrubfixture: -src and -dst are required")
		return 2
	}

	srcAbs, err := filepath.Abs(src)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: resolving -src: %v\n", err)
		return 1
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: resolving -dst: %v\n", err)
		return 1
	}
	if dstAbs == srcAbs || strings.HasPrefix(dstAbs+string(filepath.Separator), srcAbs+string(filepath.Separator)) {
		fmt.Fprintln(stderr, "scrubfixture: -dst must not be under -src")
		return 1
	}

	opts := scrub.Options{Maps: maps, Branch: branch}

	writeDst := dstAbs
	if dryRun {
		tmp, err := os.MkdirTemp("", "scrubfixture-dry-run-*")
		if err != nil {
			fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
			return 1
		}
		defer os.RemoveAll(tmp)
		writeDst = tmp
	}

	before, err := listJSONL(writeDst)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
		return 1
	}
	beforeSet := make(map[string]bool, len(before))
	for _, f := range before {
		beforeSet[f] = true
	}

	if err := scrub.Tree(srcAbs, writeDst, opts); err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
		return 1
	}

	// Only report/verify files this run actually wrote — dst may be a
	// shared parent directory (e.g. claude/projects) already holding
	// unrelated, previously-committed fixtures that Tree never touches.
	after, err := listJSONL(writeDst)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
		return 1
	}
	var written []string
	for _, f := range after {
		if !beforeSet[f] {
			written = append(written, f)
		}
	}
	sort.Strings(written)

	if len(written) == 0 {
		fmt.Fprintln(stderr, "scrubfixture: no new *.jsonl files written (dst already contained every output path)")
	}

	dirty := false
	for _, f := range written {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
			return 1
		}

		var lines []string
		if len(b) > 0 {
			lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		}

		rel, _ := filepath.Rel(writeDst, f)
		fmt.Fprintf(stderr, "%s: %d lines\n", rel, len(lines))

		for i, line := range lines {
			if findings := scrub.Findings([]byte(line)); findings != nil {
				dirty = true
				fmt.Fprintf(stderr, "  line %d: findings remain: %v\n", i+1, findings)
			}
		}
	}

	if dryRun {
		fmt.Fprintln(stderr, "scrubfixture: dry run, nothing written")
		if dirty {
			return 1
		}
		return 0
	}

	if dirty && !force {
		fmt.Fprintln(stderr, "scrubfixture: findings remain in output; rerun with -force to write anyway, or add -map rules")
		return 1
	}

	return 0
}

func listJSONL(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
