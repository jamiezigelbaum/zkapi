//go:build linux

package zkapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const companionCustodyPlatformSupported = true

type linuxCompanionProcessIdentity struct {
	pidfd int
	dead  bool
}

func newCompanionProcessIdentity(pid int) (companionProcessIdentity, error) {
	// The supervisor calls Activate immediately after Start and BEFORE Wait.
	// An exited child is therefore still an unreaped zombie, not a reusable PID.
	// pidfd_open atomically pins that lifetime; no kill(pid, 0) authority is used.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	p := &linuxCompanionProcessIdentity{pidfd: fd}
	if dead, err := p.exited(); err != nil || dead {
		p.close()
		return nil, errors.New("companion process identity is unavailable")
	}
	return p, nil
}

func (p *linuxCompanionProcessIdentity) exited() (bool, error) {
	if p.dead {
		return true, nil
	}
	if p.pidfd < 0 {
		return true, errors.New("companion pidfd is closed")
	}
	fds := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, 0); err != nil {
		return false, err
	}
	if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		return false, errors.New("companion pidfd inspection failed")
	}
	if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
		p.dead = true
	}
	return p.dead, nil
}

func (p *linuxCompanionProcessIdentity) close() {
	if p.pidfd >= 0 {
		_ = unix.Close(p.pidfd)
		p.pidfd = -1
	}
	p.dead = true
}

func companionOwnsConnectedSocket(ctx context.Context, pid int, local, remote *net.TCPAddr) (bool, error) {
	wantLocal, ok := tcpAddrPort(remote)
	if !ok {
		return false, nil
	}
	wantRemote, ok := tcpAddrPort(local)
	if !ok {
		return false, nil
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Hold an inspection pidfd as well as the Activate lifetime. The shared
	// DialContext polls the ORIGINAL lifetime before and after this inspection,
	// so even a PID recycled before this open cannot acquire its authority.
	identity, err := newCompanionProcessIdentity(pid)
	if err != nil {
		return false, err
	}
	defer identity.close()
	p := identity.(*linuxCompanionProcessIdentity)
	info, err := readCustodyProcFile(fmt.Sprintf("/proc/self/fdinfo/%d", p.pidfd))
	if err != nil {
		return false, err
	}
	procPID, err := pidfdProcPID(info)
	if err != nil {
		return false, err
	}
	// The launcher has a new PID namespace but deliberately inherits the host
	// proc mount. fdinfo's Pid is relative to THAT proc mount, unlike cmd.Pid.
	procPath := filepath.Join("/proc", strconv.Itoa(procPID))
	for attempt := 0; attempt < 20; attempt++ {
		if err := verifyCtx.Err(); err != nil {
			return false, err
		}
		if dead, err := p.exited(); err != nil || dead {
			return false, errors.New("companion exited during inspection")
		}
		owned, err := inspectCompanionProc(verifyCtx, procPath, wantLocal, wantRemote)
		if err != nil {
			return false, err
		}
		if dead, err := p.exited(); err != nil || dead {
			return false, errors.New("companion exited during inspection")
		}
		if owned {
			return true, nil
		}
		// connect can finish before accept installs the child's socket fd. Retry
		// only absence, never an inspection failure, without sending any bytes.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-verifyCtx.Done():
			timer.Stop()
			return false, verifyCtx.Err()
		case <-timer.C:
		}
	}
	return false, nil
}

func pidfdProcPID(info []byte) (int, error) {
	pid := 0
	for _, line := range strings.Split(string(info), "\n") {
		if !strings.HasPrefix(line, "Pid:") {
			continue
		}
		fields := strings.Fields(line)
		if pid != 0 || len(fields) != 2 {
			return 0, errors.New("invalid pidfd process identity")
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n <= 0 {
			return 0, errors.New("pidfd process is not visible")
		}
		pid = n
	}
	if pid == 0 {
		return 0, errors.New("missing pidfd process identity")
	}
	return pid, nil
}

const custodyProcLimit = 8 << 20

func readCustodyProcFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, custodyProcLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > custodyProcLimit {
		return nil, errors.New("companion inspection exceeds size limit")
	}
	return data, nil
}

func inspectCompanionProc(ctx context.Context, procPath string, local, remote netip.AddrPort) (bool, error) {
	dir, err := os.Open(filepath.Join(procPath, "fd"))
	if err != nil {
		return false, err
	}
	entries, err := dir.ReadDir(65537)
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if len(entries) > 65536 {
		return false, errors.New("companion descriptor limit exceeded")
	}
	inodes := make(map[uint64]bool)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		target, err := os.Readlink(filepath.Join(procPath, "fd", entry.Name()))
		// Descriptor close during enumeration is ordinary; permission or other
		// inspection errors are not evidence of ownership and must refuse.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !strings.HasPrefix(target, "socket:[") {
			continue
		}
		if !strings.HasSuffix(target, "]") {
			return false, errors.New("invalid socket inode")
		}
		inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), 10, 64)
		if err != nil || inode == 0 {
			return false, errors.New("invalid socket inode")
		}
		inodes[inode] = true
	}
	for _, table := range []string{"tcp", "tcp6"} {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		data, err := readCustodyProcFile(filepath.Join(procPath, "net", table))
		// Kernels built without IPv6 have no tcp6. tcp is always required.
		if table == "tcp6" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		owned, err := procTCPHasSocket(ctx, strings.NewReader(string(data)), inodes, local, remote, binary.NativeEndian)
		if err != nil || owned {
			return owned, err
		}
	}
	return false, nil
}

func tcpAddrPort(address *net.TCPAddr) (netip.AddrPort, bool) {
	if address == nil || address.Zone != "" {
		return netip.AddrPort{}, false
	}
	ip, ok := netip.AddrFromSlice(address.IP)
	if !ok || address.Port < 1 || address.Port > 65535 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(address.Port)), true
}

// /proc/net/tcp* prints each address as native-endian uint32 words, including
// IPv6. Ports are hexadecimal numeric values. Unmap makes dual-stack peers
// compare equal to net.TCPAddr's IPv4 representation.
func procTCPAddress(field string, order binary.ByteOrder) (netip.AddrPort, error) {
	parts := strings.Split(field, ":")
	if len(parts) != 2 || (len(parts[0]) != 8 && len(parts[0]) != 32) || len(parts[1]) != 4 {
		return netip.AddrPort{}, errors.New("invalid TCP endpoint")
	}
	port, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil || port == 0 {
		return netip.AddrPort{}, errors.New("invalid TCP port")
	}
	raw := make([]byte, len(parts[0])/2)
	for i := 0; i < len(parts[0]); i += 8 {
		word, err := strconv.ParseUint(parts[0][i:i+8], 16, 32)
		if err != nil {
			return netip.AddrPort{}, errors.New("invalid TCP address")
		}
		order.PutUint32(raw[i/2:i/2+4], uint32(word))
	}
	ip, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, errors.New("invalid TCP address")
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(port)), nil
}

func procTCPHasSocket(ctx context.Context, reader io.Reader, inodes map[uint64]bool, local, remote netip.AddrPort, order binary.ByteOrder) (bool, error) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "local_address") || !strings.Contains(scanner.Text(), "inode") {
		return false, errors.New("invalid TCP table header")
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return false, errors.New("invalid TCP table row")
		}
		if fields[3] != "01" {
			continue
		} // ESTABLISHED only, never LISTEN or TIME_WAIT.
		left, err := procTCPAddress(fields[1], order)
		if err != nil {
			return false, err
		}
		right, err := procTCPAddress(fields[2], order)
		if err != nil {
			return false, err
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			return false, errors.New("invalid TCP table inode")
		}
		if inode != 0 && inodes[inode] && left == local && right == remote {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, ctx.Err()
}
