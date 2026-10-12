//go:build linux && amd64 && !qualification

package main

import "testing"

func TestProductionCannotAblate(t *testing.T) {
	t.Setenv("ZKAPI_CONFINE_QUALIFICATION_ABLATE", "netns,pidns,landlock,landlock-net,landlock-unix,landlock-scope,ipv6-off,seccomp,caps,dumpable,harden0")
	for _, layer := range []string{"netns", "pidns", "landlock", "landlock-net", "landlock-unix", "landlock-scope", "ipv6-off", "seccomp", "caps", "dumpable", "harden0"} {
		if ablated(layer) {
			t.Fatalf("release can ablate %s", layer)
		}
	}
	for _, command := range []string{"escape", "holdlisten", "sleeper", "dumpfilter", "--ablate", "--fault", "--abi"} {
		if got := qualificationCommand(command, nil); got != 64 {
			t.Fatalf("production accepted %s", command)
		}
	}
}
