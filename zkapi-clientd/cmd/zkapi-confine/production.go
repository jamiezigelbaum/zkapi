//go:build linux && amd64 && !qualification

package main

import (
	"fmt"
	"os"
)

// Constants after inlining: no environment or argv can disable a layer.
func ablated(string) bool { return false }
func qualificationCommand(string, []string) int {
	fmt.Fprintln(os.Stderr, "unknown command")
	return 64
}
