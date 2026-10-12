//go:build linux && amd64

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestDescriptorSanitization(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"0", "1", "2", "3", "9", "nonnumeric"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                      string
		closeErr, readErr, setErr error
		wantErr                   bool
		wantFDs                   []int
	}{
		{name: "close_range succeeds"},
		{name: "ENOSYS fallback", closeErr: syscall.ENOSYS, wantFDs: []int{3, 9}},
		{name: "EINVAL fallback", closeErr: syscall.EINVAL, wantFDs: []int{3, 9}},
		{name: "denied fallback works", closeErr: syscall.EPERM, wantFDs: []int{3, 9}},
		{name: "proc unreadable refuses", closeErr: syscall.ENOSYS, readErr: syscall.EACCES, wantErr: true},
		{name: "fcntl EPERM refuses", closeErr: syscall.ENOSYS, setErr: syscall.EPERM, wantErr: true, wantFDs: []int{3}},
		{name: "fcntl EINVAL refuses", closeErr: syscall.ENOSYS, setErr: syscall.EINVAL, wantErr: true, wantFDs: []int{3}},
		{name: "closed readdir descriptor tolerated", closeErr: syscall.ENOSYS, setErr: syscall.EBADF, wantFDs: []int{3, 9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fds []int
			err := sanitizeDescriptors(func() error { return tc.closeErr }, func(path string) ([]os.DirEntry, error) {
				if tc.closeErr == nil {
					t.Fatal("fallback invoked after successful close_range")
				}
				if path != "/proc/self/fd" {
					t.Fatal(path)
				}
				return entries, tc.readErr
			}, func(fd int) error { fds = append(fds, fd); return tc.setErr })
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v", err)
			}
			if tc.wantErr {
				want := tc.readErr
				if want == nil {
					want = tc.setErr
				}
				if !errors.Is(err, want) {
					t.Fatalf("lost errno: %v", err)
				}
			}
			if !reflect.DeepEqual(fds, tc.wantFDs) {
				t.Fatalf("fcntl fds %v want %v", fds, tc.wantFDs)
			}
		})
	}
}
