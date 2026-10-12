//go:build linux && amd64

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
)

func TestCheckClassification(t *testing.T) {
	for _, tc := range []struct {
		name, events, code string
		runErr             error
		want               int
	}{
		{name: "failed clone", runErr: syscall.EPERM, code: "namespace_unavailable", want: 78},
		{name: "exec cannot start", runErr: syscall.ENOENT, code: "namespace_unavailable", want: 78},
		{name: "refusal", events: `{"event":"refused","code":"landlock_unavailable","detail":"ABI 3"}`, runErr: syscall.EPERM, code: "landlock_unavailable", want: 78},
		{name: "descriptor failure", events: `{"event":"refused","code":"descriptor_sanitize_failed","detail":"EPERM"}`, runErr: syscall.EPERM, code: "descriptor_sanitize_failed", want: 78},
		{name: "ready but failed child", events: `{"event":"ready"}`, runErr: syscall.EPERM, code: "namespace_unavailable", want: 78},
		{name: "missing report", want: 78, code: "namespace_unavailable"},
		{name: "malformed report", events: `{broken`, want: 78, code: "namespace_unavailable"},
		{name: "ready", events: `{"event":"ready","landlockAbi":9,"layers":["netns"],"stage0Layers":["nnp"]}`, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := checkWithRun(func(cmd *exec.Cmd) error {
				want := []string{"run", "--status-fd", "3", "--connect-port", "1", "--", cmd.Path, "noop"}
				if !reflect.DeepEqual(cmd.Args[1:], want) || len(cmd.ExtraFiles) != 1 {
					t.Fatalf("wrong check launch: %v", cmd.Args)
				}
				if tc.events != "" {
					fmt.Fprintln(cmd.ExtraFiles[0], "zkapi-confine: "+tc.events)
				}
				return tc.runErr // failing clone seam, without calling the kernel
			}, &out)
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if got != tc.want || result["ok"] != (tc.want == 0) {
				t.Fatalf("exit %d: %s", got, &out)
			}
			if tc.code != "" && result["code"] != tc.code {
				t.Fatalf("classification: %s", &out)
			}
			if tc.want == 0 && (result["landlockAbi"] != float64(9) || !reflect.DeepEqual(result["stage0Layers"], []any{"nnp"})) {
				t.Fatal(result)
			}
		})
	}
}

func TestPlatform(t *testing.T) {
	for _, tc := range []struct {
		os, arch string
		want     bool
	}{{"linux", "amd64", true}, {"linux", "arm64", false}, {"darwin", "amd64", false}, {"windows", "amd64", false}, {"linux", "386", false}} {
		if got := supportedPlatform(tc.os, tc.arch); got != tc.want {
			t.Fatalf("%s/%s: %t", tc.os, tc.arch, got)
		}
	}
}
