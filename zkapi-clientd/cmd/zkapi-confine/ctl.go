//go:build linux && amd64

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// The control channel between stage0 (host namespace) and stage1 (inside the
// namespace) is an anonymous SOCK_SEQPACKET pair created before the clone. It
// carries small JSON messages and, with SCM_RIGHTS, the accepted connections
// the two stages hand each other. No pathname is resolved on either side after
// the daemon starts, so nothing the daemon can rename or replace is ever
// followed by a launcher stage.

const (
	msgMax            = 8192
	msgCmsgCloexec    = 0x40000000 // MSG_CMSG_CLOEXEC
	maxPassedPerEntry = 1
)

type ctlConn struct {
	f   *os.File
	fd  int
	wmu sync.Mutex
}

func newCtlPair() (*os.File, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "ctl-host"), os.NewFile(uintptr(fds[1]), "ctl-ns"), nil
}

func newCtl(f *os.File) *ctlConn { return &ctlConn{f: f, fd: int(f.Fd())} }

func (c *ctlConn) send(msg map[string]any, file *os.File) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	var oob []byte
	if file != nil {
		oob = syscall.UnixRights(int(file.Fd()))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return syscall.Sendmsg(c.fd, b, oob, nil, 0)
}

var errCtlClosed = errors.New("control channel closed")

// recv blocks for one message. A passed descriptor, if any, is returned
// close-on-exec.
func (c *ctlConn) recv() (map[string]any, *os.File, error) {
	p := make([]byte, msgMax)
	oob := make([]byte, syscall.CmsgSpace(4*maxPassedPerEntry))
	for {
		n, oobn, flags, _, err := syscall.Recvmsg(c.fd, p, oob, msgCmsgCloexec)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if n == 0 && oobn == 0 {
			return nil, nil, errCtlClosed
		}
		var file *os.File
		if oobn > 0 {
			msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn])
			if perr != nil {
				return nil, nil, perr
			}
			for _, m := range msgs {
				fds, ferr := syscall.ParseUnixRights(&m)
				if ferr != nil {
					return nil, nil, ferr
				}
				for i, fd := range fds {
					if i == 0 && file == nil {
						file = os.NewFile(uintptr(fd), "passed")
					} else {
						syscall.Close(fd)
					}
				}
			}
		}
		if flags&syscall.MSG_TRUNC != 0 || flags&syscall.MSG_CTRUNC != 0 {
			if file != nil {
				file.Close()
			}
			return nil, nil, fmt.Errorf("control message truncated")
		}
		var msg map[string]any
		if err := json.Unmarshal(p[:n], &msg); err != nil {
			if file != nil {
				file.Close()
			}
			return nil, nil, err
		}
		return msg, file, nil
	}
}

// releaseStage1 preserves the harden-before-go ordering and kills the waiting
// child on every failure. Tests inject hardening failure without weakening argv.
func releaseStage1(ctl *ctlConn, harden func() ([]string, error), stop func()) ([]string, error) {
	layers, err := harden()
	if err != nil {
		stop()
		return nil, err
	}
	if err := ctl.send(map[string]any{"t": "go"}, nil); err != nil {
		stop()
		return nil, goSendError{err}
	}
	return layers, nil
}

func awaitGo(ctl *ctlConn) error {
	msg, f, err := ctl.recv()
	if f != nil {
		f.Close()
	}
	if err != nil {
		return err
	}
	if msg["t"] != "go" {
		return fmt.Errorf("stage0 did not send go")
	}
	return nil
}

// Distinguish control-send failure from hardening refusal as in the prototype.
type goSendError struct{ error }
