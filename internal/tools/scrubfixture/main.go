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
	"slices"
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

	if len(written) == 0 {
		fmt.Fprintf(stderr, "scrubfixture: no files found under -src %s; nothing written\n", srcAbs)
		return 1
	}

	warnUnappliedPathMaps(maps, written, writeDst, stderr)

	dirty, err := verify(written, writeDst, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "scrubfixture: %v\n", err)
		return 1
	}

	if dryRun {
		fmt.Fprintln(stderr, "scrubfixture: dry run, nothing written under -dst")
		if dirty && !force {
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

// verify re-scans every file Tree wrote this run and reports whether any
// sensitive content remains. It checks each file whole first: no
// scrub.Findings detector pattern can match across a newline, so a clean
// whole-file scan is exactly equivalent to a clean per-line scan, for
// *.jsonl and copied-through non-.jsonl files alike -- the CLI does not
// need to know which is which. Only a dirty file gets re-split into lines,
// purely to attach a line number to the report.
func verify(written []string, base string, stderr io.Writer) (dirty bool, err error) {
	for _, f := range written {
		rel, relErr := filepath.Rel(base, f)
		if relErr != nil {
			rel = f
		}

		b, err := os.ReadFile(f)
		if err != nil {
			return dirty, err
		}

		if scrub.Findings(b) == nil {
			fmt.Fprintf(stderr, "%s: clean\n", rel)
			continue
		}

		dirty = true
		fmt.Fprintf(stderr, "%s: findings remain\n", rel)
		for i, line := range strings.Split(string(b), "\n") {
			if lineFindings := scrub.Findings([]byte(line)); lineFindings != nil {
				fmt.Fprintf(stderr, "  line %d: findings remain: %v\n", i+1, lineFindings)
			}
		}
	}
	return dirty, nil
}

// warnUnappliedPathMaps reports -map rules that look like directory
// renames but left no trace in any output path.
//
// Claude encodes a project directory name from the cwd by replacing every
// non-alphanumeric character with "-", while scrub.Tree's path renaming
// rewrites only "/". So a -map whose Old contains any other character (the
// "." in mktemp's default tmp.XXXXXXXXXX template is the easy mistake)
// silently fails to rename, and the fixture lands under a directory
// carrying the generating machine's real path. Contents are scrubbed
// either way, so verify still reports "clean" -- nothing else notices.
//
// This is a warning, not a failure: a path-shaped -map may legitimately
// target only text inside records (a path mentioned in a tool result)
// rather than the session's own cwd.
func warnUnappliedPathMaps(maps []scrub.Rule, written []string, base string, stderr io.Writer) {
	for _, m := range maps {
		if !strings.HasPrefix(m.Old, "/") {
			continue // not a path rule; nothing to rename
		}
		encoded := strings.ReplaceAll(m.New, "/", "-")
		if slices.ContainsFunc(written, func(f string) bool {
			rel, err := filepath.Rel(base, f)
			return err == nil && strings.Contains(rel, encoded)
		}) {
			continue
		}
		fmt.Fprintf(stderr, "scrubfixture: warning: -map %s=%s did not rename any output path; "+
			"check the output directory names before committing "+
			"(-map path renaming encodes only \"/\", Claude encodes every non-alphanumeric character)\n",
			m.Old, m.New)
	}
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
	var suffix []string
	cur := path
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent, base := filepath.Dir(cur), filepath.Base(cur)
		if parent == cur {
			return "", err
		}
		suffix = append([]string{base}, suffix...)
		cur = parent
	}
}

func within(child, parent string) bool {
	sep := string(filepath.Separator)
	return strings.HasPrefix(child+sep, strings.TrimSuffix(parent, sep)+sep)
}
