// Package domain_test holds the guard that makes the domain a domain.
package domain_test

import (
	"errors"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/starvy/flagfish"

// internal/domain imports nothing. Not the database, not HTTP, not River.
//
// This is non-negotiable rule 1, and it is what makes the invariant tests fast and
// total: a pure package cannot be slow, cannot be flaky, and cannot lie to you
// about what it depends on. Without a check like this one, `domain` grows a
// *pgxpool.Pool within a month — not because anyone decided to, but because it is
// always locally convenient.
//
// Permitted: the standard library, and other internal/domain packages (the domain
// is allowed to be more than one package — account.Mode is imported by policy
// precisely so the user/team duality is expressed once, which is the whole point
// of the package).
//
// Everything else is a build failure, in a test that itself imports only stdlib.
func TestDomainImportsNothing(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}

		pkg, err := build.ImportDir(path, 0)
		if err != nil {
			var noGo *build.NoGoError
			if errors.As(err, &noGo) {
				return nil // no Go files here: nothing to check
			}
			return err // anything else is a real failure and must be loud
		}

		// Test files may import testing and the packages under test; the rule is
		// about the shipped code. pkg.Imports excludes test imports already.
		for _, imp := range pkg.Imports {
			if isStdlib(imp) {
				continue
			}
			if strings.HasPrefix(imp, modulePath+"/internal/domain/") {
				continue
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path // fall back to the absolute path; this is a message, not a decision
			}
			t.Errorf("internal/domain/%s imports %q.\n\n"+
				"internal/domain imports NOTHING outside the standard library (and its own "+
				"siblings). Not the DB, not HTTP, not River. If this package needs something "+
				"from the outside, the dependency is pointing the wrong way: define the "+
				"interface here and implement it out there.", rel, imp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// isStdlib: a standard-library import path has no dot in its first segment.
// (`net/http` is stdlib; `github.com/…` is not.) This is the same heuristic the
// go tool uses to tell the two apart.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
