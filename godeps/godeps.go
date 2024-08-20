//go:build go_mod_tidy_deps

// Package godeps is a pseudo-package for tracking Go tool dependencies that
// are not needed for build or test.
package godeps

import (
	// Used by the CI workflow.
	_ "honnef.co/go/tools/cmd/staticcheck"
)
