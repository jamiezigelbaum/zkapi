//go:build linux && amd64 && qualification

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func helper(name string, args []string) int {
	switch name {
	case "holdlisten":
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
		if err == nil {
			err = syscall.Listen(fd, 1)
		}
		out := map[string]any{}
		if err != nil {
			out["error"] = errName(err)
		} else if sa, gerr := syscall.Getsockname(fd); gerr == nil {
			out["port"] = sa.(*syscall.SockaddrInet4).Port
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
		time.Sleep(10 * time.Minute)
	case "sleeper":
		time.Sleep(10 * time.Minute)
	case "escape":
		if len(args) < 2 {
			return 64
		}
		self, _ := os.Executable()
		var c *exec.Cmd
		switch args[0] {
		case "setsid":
			c = exec.Command(self, "sleeper", args[1])
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		case "setpgid":
			c = exec.Command(self, "sleeper", args[1])
			c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		case "double":
			c = exec.Command(self, "escape", "setsid", args[1])
		default:
			return 64
		}
		if err := c.Start(); err != nil {
			fmt.Println(`{"error":"` + errName(err) + `"}`)
			return 1
		}
		fmt.Printf("{\"mode\":%q,\"pid\":%d}\n", args[0], c.Process.Pid)
	}
	return 0
}
