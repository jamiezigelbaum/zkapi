//go:build linux && amd64

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParsePortBounds(t *testing.T) {
	for _, test := range []struct {
		in   string
		want int
		ok   bool
	}{
		{in: "1", want: 1, ok: true},
		{in: "65535", want: 65535, ok: true},
		{in: "0"},
		{in: "65536"},
		{in: "-1"},
		{in: "not-a-port"},
	} {
		t.Run(test.in, func(t *testing.T) {
			got, err := parsePort(test.in)
			if test.ok {
				if err != nil || got != test.want {
					t.Fatalf("parsePort(%q) = %d, %v; want %d, nil", test.in, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("parsePort(%q) = %d, nil; want an error", test.in, got)
			}
		})
	}
}

func TestParseConfigAcceptsCompleteConfiguration(t *testing.T) {
	dir := makePrivateDir(t)
	args := []string{
		"--dir", dir,
		"--expose", "wallet-api=47002",
		"--reach", "9150=9050",
		"--connect-port", "47002",
		"--bind-port", "9150",
		"--status-fd", "3",
		"--", "/usr/bin/daemon", "--child-flag",
	}

	got, err := parseConfig(args)
	if err != nil {
		t.Fatal(err)
	}
	want := &config{
		dir:     dir,
		expose:  []exposeSpec{{name: "wallet-api", port: 47002}},
		reach:   []reachSpec{{nsPort: 9150, hostPort: 9050}},
		connect: []int{47002},
		bind:    []int{9150},
		prog:    []string{"/usr/bin/daemon", "--child-flag"},
		status:  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseConfig() = %#v; want %#v", got, want)
	}
}

func TestParseConfigRejectsInvalidNamesPortsStatusAndProgram(t *testing.T) {
	dir := makePrivateDir(t)
	valid := func(extra ...string) []string {
		args := append([]string{}, extra...)
		return append(args, "--status-fd", "3", "--", "/bin/true")
	}

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing status descriptor", args: []string{"--", "/bin/true"}},
		{name: "wrong status descriptor", args: []string{"--status-fd", "4", "--", "/bin/true"}},
		{name: "relative program", args: []string{"--status-fd", "3", "--", "bin/true"}},
		{name: "missing program", args: []string{"--status-fd", "3", "--"}},
		{name: "zero expose port", args: valid("--dir", dir, "--expose", "api=0")},
		{name: "large reach port", args: valid("--reach", "65536=9050")},
		{name: "uppercase expose name", args: valid("--dir", dir, "--expose", "API=47002")},
		{name: "path expose name", args: valid("--dir", dir, "--expose", "../api=47002")},
		{name: "leading hyphen expose name", args: valid("--dir", dir, "--expose", "-api=47002")},
		{name: "overlong expose name", args: valid("--dir", dir, "--expose", strings.Repeat("a", 33)+"=47002")},
		{name: "expose without directory", args: valid("--expose", "api=47002")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if cfg, err := parseConfig(test.args); err == nil {
				t.Fatalf("parseConfig(%q) = %#v, nil; want an error", test.args, cfg)
			}
		})
	}
}

func TestParseConfigRequiresPrivateRealDirectory(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		dir := makePrivateDir(t)
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		assertConfigDirRejected(t, dir)
	})

	t.Run("symlink", func(t *testing.T) {
		dir := makePrivateDir(t)
		link := filepath.Join(t.TempDir(), "socket-dir-link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		assertConfigDirRejected(t, link)
	})

	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "socket-dir")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		assertConfigDirRejected(t, path)
	})
}

func makePrivateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "socket-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertConfigDirRejected(t *testing.T, dir string) {
	t.Helper()
	args := []string{"--dir", dir, "--expose", "api=47002", "--status-fd", "3", "--", "/bin/true"}
	if cfg, err := parseConfig(args); err == nil {
		t.Fatalf("parseConfig(%q) = %#v, nil; want an error", args, cfg)
	}
}
