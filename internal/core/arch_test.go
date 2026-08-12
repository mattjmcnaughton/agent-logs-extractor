// Package core holds pure use cases and domain services. This test
// mechanically enforces the purity rule from docs/architecture.md: nothing
// under internal/core/ may import infrastructure.
package core

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/mattjmcnaughton/agent-logs-extractor"

// TestCorePurity asserts that no non-test file under internal/core/ imports
// internal/adapters/..., os, net/http, os/exec, or any third-party package.
// Allowed: the rest of the stdlib, internal/ports, and internal/core itself.
//
// Known limitation, deliberately accepted: this only checks direct imports
// of files under internal/core/; taint THROUGH internal/ports is not
// caught (a port implementation is free to import os/exec, and a core file
// that only imports internal/ports would not see that transitively).
// internal/ports is three files of pure interface declarations whose
// imports are trivially reviewable, so this is an acceptable gap rather
// than a hole to close here.
func TestCorePurity(t *testing.T) {
	fset := token.NewFileSet()
	scanned := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if reason := forbidden(p); reason != "" {
				t.Errorf("internal/core/%s imports %q: %s", path, p, reason)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A walk that finds nothing passes vacuously, which would hide a wrong
	// walk root just as effectively as a bug in forbidden() would.
	if scanned == 0 {
		t.Fatalf("scanned 0 files under internal/core; walk root is wrong")
	}
}

// TestForbidden is a table test of the forbidden() classifier itself. Its
// job is to guard TestCorePurity against passing vacuously: a bug in
// forbidden() that always returns "" would make TestCorePurity green no
// matter what internal/core/ imports.
func TestForbidden(t *testing.T) {
	wantForbidden := []string{
		"os",
		"os/exec",
		"os/user",
		"os/signal",
		"net/http",
		"net",
		"syscall",
		"runtime/debug",
		"plugin",
		"embed",
		"github.com/spf13/cobra",
		modulePath + "/internal/adapters/cli",
		// Path-boundary regressions: a package whose name merely starts
		// with "internal/ports" or "internal/core" must not be admitted
		// just because of the string prefix.
		modulePath + "/internal/portsx",
		modulePath + "/internal/coreish",
	}
	for _, p := range wantForbidden {
		if reason := forbidden(p); reason == "" {
			t.Errorf("forbidden(%q) = \"\", want a non-empty reason", p)
		}
	}

	wantAllowed := []string{
		"context",
		"encoding/json",
		"time",
		"log/slog",
		"net/url",
		modulePath + "/internal/ports",
		modulePath + "/internal/core/model",
	}
	for _, p := range wantAllowed {
		if reason := forbidden(p); reason != "" {
			t.Errorf("forbidden(%q) = %q, want \"\"", p, reason)
		}
	}
}

// forbidden returns a non-empty reason if the import path is not allowed in
// the core.
func forbidden(p string) string {
	if p == modulePath || strings.HasPrefix(p, modulePath+"/") {
		rest := strings.TrimPrefix(p, modulePath+"/")
		// A path-boundary check, not a string-prefix check: an
		// internal/portsx or internal/coreish package must not be
		// admitted just because its name starts with "internal/ports" or
		// "internal/core".
		if rest == "internal/ports" || strings.HasPrefix(rest, "internal/ports/") ||
			rest == "internal/core" || strings.HasPrefix(rest, "internal/core/") {
			return ""
		}
		return "core may import only internal/ports and internal/core from this module"
	}
	first, _, _ := strings.Cut(p, "/")
	if strings.Contains(first, ".") {
		return "third-party packages are not allowed in the core"
	}
	// Denylist by family, not by exact string: every OS-facing stdlib
	// package under these roots hands the core an infrastructure escape
	// hatch (os/user resolves the home directory, os/signal and syscall
	// touch the process/OS directly, net opens sockets, runtime/debug
	// introspects the runtime, plugin/embed pull in artifacts). net/url is
	// pure string manipulation and stays allowed.
	switch {
	case p == "os" || strings.HasPrefix(p, "os/"):
		return "the core must not touch the OS directly; use a port"
	case p == "net" || (strings.HasPrefix(p, "net/") && p != "net/url"):
		return "the core must not touch the network directly; use a port"
	case p == "syscall" || strings.HasPrefix(p, "syscall/"):
		return "the core must not make raw syscalls; use a port"
	case p == "runtime/debug":
		return "the core must not introspect the runtime; use a port"
	case p == "plugin":
		return "the core must not load plugins; use a port"
	case p == "embed":
		return "the core must not embed files; use a port"
	}
	return ""
}
