// Package scrub strips or replaces sensitive text in vendor JSONL log lines
// while preserving their structure byte-for-byte where nothing matches.
//
// Substitution operates on raw bytes, never through encoding/json
// marshalling: round-tripping through json.Marshal would reorder keys and
// reformat numbers, breaking the "verbatim ground truth" property fixtures
// depend on. JSON decoding is used only to validate the line before and
// after scrubbing.
package scrub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rule is a literal (non-regex) replacement applied to raw line bytes.
type Rule struct {
	Old, New string
}

// Options configures a scrub pass.
type Options struct {
	// Maps are literal replacements applied first, longest Old first, so a
	// more specific rule always wins over a shorter one it contains.
	Maps []Rule
	// Branch, if non-empty, rewrites every "gitBranch":"…" value to this
	// string. Empty leaves gitBranch values untouched.
	Branch string
}

// builtin detectors, defining "sensitive" for both Line/Tree scrubbing and
// Findings verification.
var (
	reToken = regexp.MustCompile(
		`sk-ant-[A-Za-z0-9_\-]{8,}` +
			`|sk-[A-Za-z0-9]{20,}` +
			`|(?:ghp|gho|ghu|ghs)_[A-Za-z0-9]{20,}` +
			`|github_pat_[A-Za-z0-9_]{20,}` +
			`|glpat-[A-Za-z0-9_\-]{16,}` +
			`|AKIA[0-9A-Z]{16}` +
			`|xox[baprs]-[A-Za-z0-9\-]{10,}` +
			`|Bearer [A-Za-z0-9._\-]{20,}`,
	)
	reEmail     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reHomeSeg   = regexp.MustCompile(`/home/([A-Za-z0-9_.\-]+)`)
	reUsersSeg  = regexp.MustCompile(`/Users/([A-Za-z0-9_.\-]+)`)
	reRoot      = regexp.MustCompile(`/root([^A-Za-z0-9_.\-])`)
	reGitBranch = regexp.MustCompile(`"gitBranch":"((?:[^"\\]|\\.)*)"`)
)

// Line scrubs one JSONL line, returning the scrubbed bytes.
//
// Errors if the input is not a JSON object, if the output no longer parses,
// or if the top-level key set or the "type" value changed.
func Line(b []byte, o Options) ([]byte, error) {
	if err := validate(o); err != nil {
		return nil, err
	}

	before, beforeType, err := decodeObject(b)
	if err != nil {
		return nil, fmt.Errorf("scrub: input is not a JSON object: %w", err)
	}

	out := append([]byte(nil), b...)
	out = applyMaps(out, o.Maps)
	out = redactBuiltins(out)
	out = rewriteGitBranch(out, o.Branch)

	after, afterType, err := decodeObject(out)
	if err != nil {
		return nil, fmt.Errorf("scrub: output is not valid JSON: %w", err)
	}

	if len(before) != len(after) {
		return nil, fmt.Errorf("scrub: top-level key count changed from %d to %d", len(before), len(after))
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			return nil, fmt.Errorf("scrub: top-level key %q was removed", k)
		}
	}
	if _, ok := before["type"]; ok && beforeType != afterType {
		return nil, fmt.Errorf("scrub: \"type\" value changed from %q to %q", beforeType, afterType)
	}

	return out, nil
}

// Findings returns the sensitive substrings still present in b (nil if
// clean). Used both by the CLI (post-scrub verification) and by the
// fixture guard test.
func Findings(b []byte) []string {
	var out []string

	out = append(out, matchAll(reToken, b)...)
	for _, m := range reEmail.FindAll(b, -1) {
		if string(m) != "user@example.com" {
			out = append(out, string(m))
		}
	}
	out = append(out, matchAll(reRoot, b)...)

	for _, m := range reHomeSeg.FindAllSubmatch(b, -1) {
		if string(m[1]) != "user" {
			out = append(out, string(m[0]))
		}
	}
	for _, m := range reUsersSeg.FindAllSubmatch(b, -1) {
		if string(m[1]) != "user" {
			out = append(out, string(m[0]))
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// Tree walks src (a file or directory), scrubs every *.jsonl line, and
// writes the mirrored tree under dst. Maps are additionally applied to
// output paths in Claude's encoded form (strings.ReplaceAll(p, "/", "-")),
// so the encoded-cwd directory name and any nested subtree (e.g.
// <uuid>/subagents/) are renamed to match the scrubbed cwd. Non-.jsonl
// files are copied unchanged.
func Tree(src, dst string, o Options) error {
	if err := validate(o); err != nil {
		return err
	}

	items, err := collect(src)
	if err != nil {
		return err
	}

	for _, it := range items {
		outRel := renamePath(it.rel, o.Maps)
		outPath := joinPath(dst, outRel)
		if err := ensureDir(dirOf(outPath)); err != nil {
			return err
		}
		if strings.HasSuffix(it.abs, ".jsonl") {
			if err := scrubFile(it.abs, outPath, o); err != nil {
				return fmt.Errorf("scrub: %s: %w", it.rel, err)
			}
		} else {
			if err := copyFile(it.abs, outPath); err != nil {
				return fmt.Errorf("scrub: %s: %w", it.rel, err)
			}
		}
	}
	return nil
}

func matchAll(re *regexp.Regexp, b []byte) []string {
	var out []string
	for _, m := range re.FindAll(b, -1) {
		out = append(out, string(m))
	}
	return out
}

func decodeObject(b []byte) (map[string]json.RawMessage, string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", err
	}
	var typ string
	if raw, ok := m["type"]; ok {
		_ = json.Unmarshal(raw, &typ)
	}
	return m, typ, nil
}

func validate(o Options) error {
	if strings.ContainsAny(o.Branch, `"\`) {
		return fmt.Errorf("scrub: Options.Branch %q must not contain '\"' or '\\'", o.Branch)
	}
	for _, r := range o.Maps {
		if r.Old == "" {
			return fmt.Errorf("scrub: Rule.Old must not be empty")
		}
		if strings.ContainsAny(r.New, `"\`) {
			return fmt.Errorf("scrub: Rule.New %q must not contain '\"' or '\\'", r.New)
		}
	}
	return nil
}

func applyMaps(b []byte, maps []Rule) []byte {
	if len(maps) == 0 {
		return b
	}
	sorted := append([]Rule(nil), maps...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Old) > len(sorted[j].Old) })
	for _, r := range sorted {
		b = bytes.ReplaceAll(b, []byte(r.Old), []byte(r.New))
	}
	return b
}

func redactBuiltins(b []byte) []byte {
	b = reToken.ReplaceAll(b, []byte("REDACTED"))
	b = reEmail.ReplaceAll(b, []byte("user@example.com"))
	b = reRoot.ReplaceAll(b, []byte("/home/user$1"))
	b = reHomeSeg.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := reHomeSeg.FindSubmatch(m)
		if string(sub[1]) == "user" {
			return m
		}
		return []byte("/home/user")
	})
	b = reUsersSeg.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := reUsersSeg.FindSubmatch(m)
		if string(sub[1]) == "user" {
			return m
		}
		return []byte("/home/user")
	})
	return b
}

func rewriteGitBranch(b []byte, branch string) []byte {
	if branch == "" {
		return b
	}
	return reGitBranch.ReplaceAll(b, []byte(`"gitBranch":"`+branch+`"`))
}
