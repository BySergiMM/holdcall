//go:build !darwin && !windows && !linux

package credential

import (
	"fmt"
	"runtime"
)

// New fails on platforms with no known secure credential mechanism wired up
// yet, rather than silently falling back to something weaker.
func New() (Store, error) {
	return nil, fmt.Errorf("no credential store implemented for %s/%s", runtime.GOOS, runtime.GOARCH)
}
