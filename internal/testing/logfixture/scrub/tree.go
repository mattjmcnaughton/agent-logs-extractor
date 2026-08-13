package scrub

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// item is one file discovered under Tree's src, with its path relative to
// filepath.Dir(src) — i.e. including src's own basename as the first
// component, so a project-directory rename (the encoded-cwd segment) is
// just another path replacement.
type item struct {
	abs string
	rel string
}

func collect(src string) ([]item, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("scrub: stat %s: %w", src, err)
	}

	if !info.IsDir() {
		return []item{{abs: src, rel: filepath.Base(src)}}, nil
	}

	root := filepath.Dir(src)
	var items []item
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		items = append(items, item{abs: p, rel: rel})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scrub: walking %s: %w", src, err)
	}
	return items, nil
}

// renamePath applies Maps to rel in Claude's encoded form
// (strings.ReplaceAll(p, "/", "-")), longest Old first.
func renamePath(rel string, maps []Rule) string {
	if len(maps) == 0 {
		return rel
	}
	sorted := append([]Rule(nil), maps...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Old) > len(sorted[j].Old) })
	for _, r := range sorted {
		oldEnc := strings.ReplaceAll(r.Old, "/", "-")
		newEnc := strings.ReplaceAll(r.New, "/", "-")
		rel = strings.ReplaceAll(rel, oldEnc, newEnc)
	}
	return rel
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// scrubFile scrubs every line of srcPath and writes the result to dstPath,
// preserving line count, order, and a missing trailing newline. A blank
// line (which is not valid JSON on its own, so Line would reject it) is
// passed through verbatim rather than aborting the whole file.
func scrubFile(srcPath, dstPath string, o Options) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()

	r := bufio.NewReaderSize(in, 64*1024)
	lineNum := 0
	for {
		raw, readErr := r.ReadString('\n')
		if len(raw) == 0 && readErr != nil {
			break
		}
		lineNum++

		hasNL := strings.HasSuffix(raw, "\n")
		content := raw
		if hasNL {
			content = content[:len(content)-1]
		}

		scrubbed := []byte(content)
		if content != "" {
			scrubbed, err = Line([]byte(content), o)
			if err != nil {
				return fmt.Errorf("line %d: %w", lineNum, err)
			}
		}
		if _, err := out.Write(scrubbed); err != nil {
			return err
		}
		if hasNL {
			if _, err := out.Write([]byte("\n")); err != nil {
				return err
			}
		}

		if readErr != nil {
			break
		}
	}
	return nil
}
