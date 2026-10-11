//go:build darwin

package zkapi

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const companionCustodyPlatformSupported = true

type darwinCompanionProcessIdentity struct {
	kqueue int
	dead   bool
}

func newCompanionProcessIdentity(pid int) (companionProcessIdentity, error) {
	kqueue, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(kqueue, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kqueue)
		return nil, err
	}
	return &darwinCompanionProcessIdentity{kqueue: kqueue}, nil
}

func (p *darwinCompanionProcessIdentity) exited() (bool, error) {
	if p.dead {
		return true, nil
	}
	events := make([]unix.Kevent_t, 1)
	timeout := unix.Timespec{}
	n, err := unix.Kevent(p.kqueue, nil, events, &timeout)
	if n != 0 {
		p.dead = true
	}
	return p.dead, err
}

func (p *darwinCompanionProcessIdentity) close() { _ = unix.Close(p.kqueue) }

func companionOwnsConnectedSocket(ctx context.Context, pid int, local, remote *net.TCPAddr) (bool, error) {
	wantLocal, ok := tcpAddrPort(remote)
	if !ok {
		return false, nil
	}
	wantRemote, ok := tcpAddrPort(local)
	if !ok {
		return false, nil
	}
	// A caller may have no deadline. Keep process retirement bounded even if
	// lsof itself stalls while DialContext holds the custody mutex.
	verifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// connect(2) may complete just before accept(2) installs the peer's file
	// descriptor. Bound the ownership probe without ever sending bridge bytes.
	for attempt := 0; attempt < 20; attempt++ {
		output, err := exec.CommandContext(verifyCtx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:ESTABLISHED", "-F0pnT").Output()
		if err == nil && lsofHasSocket(output, pid, wantLocal, wantRemote) {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if verifyCtx.Err() != nil {
			return false, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-verifyCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		case <-timer.C:
		}
	}
	return false, nil
}

func tcpAddrPort(address *net.TCPAddr) (netip.AddrPort, bool) {
	ip, ok := netip.AddrFromSlice(address.IP)
	if !ok || address.Port < 1 || address.Port > 65535 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(address.Port)), true
}

func lsofHasSocket(output []byte, pid int, wantLocal, wantRemote netip.AddrPort) bool {
	seenPID := false
	for _, raw := range bytes.Split(output, []byte{0}) {
		field := strings.TrimSpace(string(raw))
		if len(field) < 2 {
			continue
		}
		switch field[0] {
		case 'p':
			seenPID = field[1:] == strconv.Itoa(pid)
		case 'n':
			if !seenPID {
				continue
			}
			parts := strings.Split(field[1:], "->")
			if len(parts) != 2 {
				continue
			}
			local, localErr := netip.ParseAddrPort(parts[0])
			remote, remoteErr := netip.ParseAddrPort(parts[1])
			if localErr == nil && remoteErr == nil && local.Addr().Unmap() == wantLocal.Addr() && local.Port() == wantLocal.Port() && remote.Addr().Unmap() == wantRemote.Addr() && remote.Port() == wantRemote.Port() {
				return true
			}
		}
	}
	return false
}
