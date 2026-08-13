// Command scrubfixture turns a real ~/.claude (or ~/.codex) session file or
// directory into a committable fixture: it scrubs sensitive text out of
// every JSONL line while preserving structure, then re-scans its own
// output and fails loudly if anything sensitive remains.
//
// It refuses to run when -src and -dst are nested inside one another (in
// either direction), so it can never write onto the source tree it was
// told about. Run via `just scrub-fixture <flags>`.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	flags := flag.NewFlagSet("scrubfixture", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		src    string
		dst    string
		branch string
		dryRun bool
		force  bool
		maps   mapFlags
	)
	flags.StringVar(&src, "src", "", "real session file or directory (read-only)")
	flags.StringVar(&dst, "dst", "", "destination directory")
	flags.Var(&maps, "map", "repeatable literal replacement OLD=NEW, applied to line bytes and (encoded) to output paths")
	flags.StringVar(&branch, "branch", "main", `gitBranch rewrite value (empty string keeps the original)`)
	flags.BoolVar(&dryRun, "dry-run", false, "scrub into a scratch directory instead of -dst; verify and report, write nothing under -dst")
	flags.BoolVar(&force, "force", false, "write even if findings remain")

	if err := flags.Parse(args); err != nil {
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

	if err := guardDisjoint(srcAbs, dstAbs); err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
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

	written, err := scrub.Tree(srcAbs, writeDst, opts)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
		return 1
	}

	dirty := false
	for _, f := range written {
		rel, _ := filepath.Rel(writeDst, f)

		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
			return 1
		}

		if !strings.HasSuffix(f, ".jsonl") {
			// Non-.jsonl files (e.g. a subagent's .meta.json sidecar) are
			// copied through verbatim rather than rewritten line-by-line,
			// but every byte Tree writes still gets scanned before this
			// tool exits 0.
			fmt.Fprintf(stderr, "%s: copied verbatim\n", rel)
			if findings := scrub.Findings(b); findings != nil {
				dirty = true
				fmt.Fprintf(stderr, "  findings remain: %v\n", findings)
			}
			continue
		}

		var lines []string
		if len(b) > 0 {
			lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		}

		fmt.Fprintf(stderr, "%s: %d lines\n", rel, len(lines))

		for i, line := range lines {
			if findings := scrub.Findings([]byte(line)); findings != nil {
				dirty = true
				fmt.Fprintf(stderr, "  line %d: findings remain: %v\n", i+1, findings)
			}
		}
	}

	if dryRun {
		fmt.Fprintln(stderr, "scrubfixture: dry run, nothing written under -dst")
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

// guardDisjoint rejects -src/-dst pairs where either resolved path is a
// prefix of the other (in either direction), including when a symlink is
// involved. -dst commonly does not exist yet (scrub.Tree creates it), so
// resolution walks up to the nearest existing ancestor rather than
// requiring the full path to exist.
func guardDisjoint(srcAbs, dstAbs string) error {
	srcReal, err := evalSymlinksBestEffort(srcAbs)
	if err != nil {
		return fmt.Errorf("resolving -src: %w", err)
	}
	dstReal, err := evalSymlinksBestEffort(dstAbs)
	if err != nil {
		return fmt.Errorf("resolving -dst: %w", err)
	}

	if srcReal == dstReal || within(dstReal, srcReal) || within(srcReal, dstReal) {
		return fmt.Errorf("-src and -dst must not be nested inside one another (src=%s dst=%s)", srcReal, dstReal)
	}
	return nil
}

// evalSymlinksBestEffort resolves as much of path as exists via
// filepath.EvalSymlinks, walking up to the nearest existing ancestor when
// path itself (or a trailing portion of it) does not exist yet, and
// rejoining the not-yet-existing suffix unresolved.
func evalSymlinksBestEffort(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	parent, base := filepath.Dir(path), filepath.Base(path)
	if parent == path {
		return "", err
	}
	resolvedParent, perr := evalSymlinksBestEffort(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, base), nil
}

func within(child, parent string) bool {
	return strings.HasPrefix(child+string(filepath.Separator), parent+string(filepath.Separator))
}
