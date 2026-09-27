//go:build !windows

package update

import "os"

// replace moves the new binary over exe in one step.
func replace(tmp, exe string) error { return os.Rename(tmp, exe) }
