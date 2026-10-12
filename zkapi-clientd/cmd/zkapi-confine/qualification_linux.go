//go:build linux && amd64 && qualification

package main

import (
	"fmt"
	"os"
	"strings"
)

// Never linked into the release binary. Only the matrix's negative controls
// can request ablation; fault injection and ABI emulation have been removed.
func ablated(layer string) bool {
	for _, name := range strings.Split(os.Getenv("ZKAPI_CONFINE_QUALIFICATION_ABLATE"), ",") {
		if name == layer {
			return true
		}
	}
	return false
}
func qualificationCommand(name string, args []string) int {
	switch name {
	case "holdlisten", "sleeper", "escape":
		return helper(name, args)
	}
	fmt.Fprintln(os.Stderr, "unknown command")
	return 64
}
