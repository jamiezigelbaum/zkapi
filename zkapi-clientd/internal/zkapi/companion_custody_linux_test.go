//go:build linux

package zkapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompanionCustodyHelperProcess(t *testing.T) {
	if os.Getenv("ZKAPI_CUSTODY_HELPER") != "1" {
		return
	}
	mode := os.Getenv("ZKAPI_CUSTODY_MODE")
	target := os.Getenv("ZKAPI_CUSTODY_TARGET")
	switch mode {
	case "listen":
		listener, err := net.Listen("tcp", target)
		if err != nil {
			os.Exit(2)
		}
		fmt.Println(listener.Addr().String())
		connection, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			os.Exit(3)
		}
		fmt.Println("accepted")
		data, err := io.ReadAll(io.LimitReader(connection, 128))
		_ = connection.Close()
		if err != nil {
			os.Exit(4)
		}
		fmt.Println("data:" + hex.EncodeToString(data))
	case "connect":
		connection, err := net.Dial("tcp", target)
		if err != nil {
			os.Exit(5)
		}
		fmt.Println("connected")
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = connection.Close()
	case "sleep":
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

type custodyHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  bytes.Buffer
}

func startCustodyHelper(t *testing.T, mode, target string) *custodyHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCompanionCustodyHelperProcess$")
	command.Env = append(os.Environ(), "ZKAPI_CUSTODY_HELPER=1", "ZKAPI_CUSTODY_MODE="+mode, "ZKAPI_CUSTODY_TARGET="+target)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	helper := &custodyHelper{command: command, stdin: stdin, stdout: bufio.NewReader(stdout)}
	command.Stderr = &helper.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = helper.stdin.Close()
		_ = helper.command.Process.Kill()
		_ = helper.command.Wait()
	})
	return helper
}

func (h *custodyHelper) line(t *testing.T) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		line, err := h.stdout.ReadString('\n')
		ready <- result{strings.TrimSpace(line), err}
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatalf("helper output ended: %v (%s)", result.err, h.stderr.String())
		}
		return result.line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for custody helper")
		return ""
	}
}

func newActiveCustody(t *testing.T, helper *custodyHelper) *CompanionCustody {
	t.Helper()
	custody, err := NewCompanionCustody()
	if err != nil {
		t.Fatal(err)
	}
	if err := custody.Activate(helper.command.Process.Pid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { custody.Retire(helper.command.Process.Pid) })
	return custody
}

type acceptedRead struct {
	accepted bool
	n        int
	err      error
}

func readFromListener(listener net.Listener) <-chan acceptedRead {
	result := make(chan acceptedRead, 1)
	go func() {
		if tcp, ok := listener.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Now().Add(2 * time.Second))
		}
		connection, err := listener.Accept()
		if err != nil {
			result <- acceptedRead{err: err}
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		buffer := make([]byte, 128)
		n, err := connection.Read(buffer)
		result <- acceptedRead{accepted: true, n: n, err: err}
	}()
	return result
}

func TestCompanionCustodySquatterReceivesZeroBytes(t *testing.T) {
	helper := startCustodyHelper(t, "sleep", "")
	if helper.line(t) != "ready" {
		t.Fatal("helper did not start")
	}
	custody := newActiveCustody(t, helper)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	read := readFromListener(listener)
	wallet, err := New(Config{ClientURL: "http://" + listener.Addr().String(), BridgeToken: strings.Repeat("synthetic-bridge-token-", 2), HTTPClient: &http.Client{}, CompanionCustody: custody})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.WalletStatus(context.Background()); err == nil {
		t.Fatal("wallet client accepted an unowned listener")
	}
	result := <-read
	if !result.accepted || result.n != 0 {
		t.Fatalf("squatting listener read %d bytes (accepted=%v, err=%v)", result.n, result.accepted, result.err)
	}
}

func TestCompanionCustodyAcceptsOwnedSocketAndSurvivesListenerSwap(t *testing.T) {
	helper := startCustodyHelper(t, "listen", "127.0.0.1:0")
	address := helper.line(t)
	custody := newActiveCustody(t, helper)
	connection, err := custody.DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if helper.line(t) != "accepted" {
		t.Fatal("owned helper did not accept connection")
	}
	replacement, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("could not install replacement listener after owned connection", err)
	}
	defer replacement.Close()
	replacementRead := readFromListener(replacement)
	payload := []byte("test-bridge-credential")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	want := "data:" + hex.EncodeToString(payload)
	if got := helper.line(t); got != want {
		t.Fatalf("owned socket got %q, want %q", got, want)
	}
	result := <-replacementRead
	if result.accepted || result.n != 0 {
		t.Fatalf("replacement listener intercepted the established socket: %+v", result)
	}
}

func TestCompanionCustodyDeniesBeforeStartAndAfterExit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	read := readFromListener(listener)
	custody, err := NewCompanionCustody()
	if err != nil {
		t.Fatal(err)
	}
	if connection, err := custody.DialContext(context.Background(), "tcp", listener.Addr().String()); err == nil {
		connection.Close()
		t.Fatal("inactive custody allowed a connection")
	}
	if result := <-read; result.accepted {
		t.Fatal("inactive custody reached the listener")
	}

	helper := startCustodyHelper(t, "sleep", "")
	if helper.line(t) != "ready" {
		t.Fatal("helper did not start")
	}
	if err := custody.Activate(helper.command.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := helper.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := helper.command.Wait(); err != nil {
		t.Fatal(err)
	}
	// Repeat after the first pidfd poll to prove the observed exit remains
	// sticky and cannot become authority for a reused PID.
	for attempt := 0; attempt < 2; attempt++ {
		read = readFromListener(listener)
		if connection, err := custody.DialContext(context.Background(), "tcp", listener.Addr().String()); err == nil {
			connection.Close()
			t.Fatal("exited child retained connection authority")
		}
		if result := <-read; result.accepted {
			t.Fatal("exited-child custody reached the listener")
		}
	}
	custody.Retire(helper.command.Process.Pid)
}

func TestCompanionCustodyMatchesExactReversedSocketTuple(t *testing.T) {
	decoy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoy.Close()
	helper := startCustodyHelper(t, "connect", decoy.Addr().String())
	decoyConnection, err := decoy.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer decoyConnection.Close()
	if helper.line(t) != "connected" {
		t.Fatal("helper did not establish decoy connection")
	}
	custody := newActiveCustody(t, helper)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	read := readFromListener(target)
	if connection, err := custody.DialContext(context.Background(), "tcp", target.Addr().String()); err == nil {
		connection.Close()
		t.Fatal("a different child socket satisfied custody")
	}
	result := <-read
	if !result.accepted || result.n != 0 {
		t.Fatalf("tuple-mismatched listener read %d bytes (accepted=%v, err=%v)", result.n, result.accepted, result.err)
	}
}

func TestProcTCPAddressFixtures(t *testing.T) {
	for _, tc := range []struct {
		wire, want string
		order      binary.ByteOrder
	}{
		{"0100007F:B79A", "127.0.0.1:47002", binary.LittleEndian},
		{"7F000001:B79A", "127.0.0.1:47002", binary.BigEndian},
		{"00000000000000000000000001000000:1234", "[::1]:4660", binary.LittleEndian},
		{"B80D0120785634120100CDAB04030201:01BB", "[2001:db8:1234:5678:abcd:1:102:304]:443", binary.LittleEndian},
		{"20010DB812345678ABCD000101020304:01BB", "[2001:db8:1234:5678:abcd:1:102:304]:443", binary.BigEndian},
		{"0000000000000000FFFF00000100007F:B79A", "127.0.0.1:47002", binary.LittleEndian},
		{"00000000000000000000FFFF7F000001:B79A", "127.0.0.1:47002", binary.BigEndian},
	} {
		got, err := procTCPAddress(tc.wire, tc.order)
		if err != nil || got.String() != tc.want {
			t.Fatalf("%s: %s %v, want %s", tc.wire, got, err, tc.want)
		}
	}
	for _, wire := range []string{"", "0100007F", "0100007F:0000", "0100007F:10000", "GG00007F:0001", "00007F:0001", "0100007F:xyz1"} {
		if _, err := procTCPAddress(wire, binary.LittleEndian); err == nil {
			t.Fatalf("accepted malformed endpoint %q", wire)
		}
	}
}

func TestProcTCPRequiresExactEstablishedOwnedTuple(t *testing.T) {
	local := netip.MustParseAddrPort("127.0.0.1:47002")
	remote := netip.MustParseAddrPort("127.0.0.1:52111")
	row := "0: 0100007F:B79A 0100007F:CB8F 01 00000000:00000000 00:00000000 00000000 1000 0 12345 1 00000000\n"
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"
	for _, tc := range []struct {
		name, row string
		inodes    map[uint64]bool
		want      bool
	}{
		{"owned", row, map[uint64]bool{12345: true}, true},
		{"foreign inode", row, map[uint64]bool{54321: true}, false},
		{"wrong server port", strings.Replace(row, "B79A", "B79B", 1), map[uint64]bool{12345: true}, false},
		{"wrong client port", strings.Replace(row, "CB8F", "CB90", 1), map[uint64]bool{12345: true}, false},
		{"wrong address", strings.Replace(row, "0100007F", "0200007F", 1), map[uint64]bool{12345: true}, false},
		{"not established", strings.Replace(row, " 01 ", " 0A ", 1), map[uint64]bool{12345: true}, false},
		{"zero inode", strings.Replace(row, "12345", "0", 1), map[uint64]bool{0: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := procTCPHasSocket(context.Background(), strings.NewReader(header+tc.row), tc.inodes, local, remote, binary.LittleEndian)
			if err != nil || got != tc.want {
				t.Fatalf("got %v %v, want %v", got, err, tc.want)
			}
		})
	}
	if got, err := procTCPHasSocket(context.Background(), strings.NewReader(header+row), map[uint64]bool{12345: true}, remote, local, binary.LittleEndian); err != nil || got {
		t.Fatalf("accepted forward tuple: %v %v", got, err)
	}
	for _, body := range []string{"", header + "truncated\n", header + strings.Replace(row, "B79A", "QQQQ", 1), header + strings.Replace(row, "12345", "bad", 1)} {
		if got, err := procTCPHasSocket(context.Background(), strings.NewReader(body), map[uint64]bool{12345: true}, local, remote, binary.LittleEndian); err == nil || got {
			t.Fatalf("accepted malformed table: %v %v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := procTCPHasSocket(ctx, strings.NewReader(header+row), map[uint64]bool{12345: true}, local, remote, binary.LittleEndian); got || !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v %v", got, err)
	}
}

func TestPidfdProcPIDUsesProcMountNamespace(t *testing.T) {
	// Namespace PID 7 is not /proc/7 on the launcher's inherited proc mount.
	if pid, err := pidfdProcPID([]byte("pos:\t0\nflags:\t02000002\nPid:\t54321\nNSpid:\t54321\t7\n")); err != nil || pid != 54321 {
		t.Fatalf("%d %v", pid, err)
	}
	for _, body := range []string{"", "NSpid:\t123\n", "Pid:\t-1\n", "Pid:\t0\n", "Pid:\t1 2\n", "Pid:\t1\nPid:\t2\n"} {
		if _, err := pidfdProcPID([]byte(body)); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestCompanionCustodyInspectionFailureSendsNoBytes(t *testing.T) {
	helper := startCustodyHelper(t, "sleep", "")
	helper.line(t)
	custody := newActiveCustody(t, helper)
	// Original lifetime remains live, but opening the inspection pidfd fails.
	custody.active.pid = -1
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	read := readFromListener(listener)
	wallet, err := New(Config{ClientURL: "http://" + listener.Addr().String(), BridgeToken: "synthetic-inspection-failure-token", HTTPClient: &http.Client{}, CompanionCustody: custody})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.WalletStatus(context.Background()); err == nil {
		t.Fatal("inspection failure accepted")
	}
	result := <-read
	if !result.accepted || result.n != 0 {
		t.Fatalf("inspection failure sent bytes: %+v", result)
	}
	custody.active.pid = helper.command.Process.Pid
}

func TestCompanionCustodyDeadIdentityCannotTransferToLivePID(t *testing.T) {
	helper := startCustodyHelper(t, "sleep", "")
	helper.line(t)
	custody := newActiveCustody(t, helper)
	helper.stdin.Close()
	if err := helper.command.Wait(); err != nil {
		t.Fatal(err)
	}
	replacement := startCustodyHelper(t, "sleep", "")
	replacement.line(t)
	originalPID := custody.active.pid
	// Deterministic PID-reuse model: the lookup now names a live process; the
	// kernel identity must still reject it. Actual PID recycling isn't forced.
	custody.active.pid = replacement.command.Process.Pid
	for i := 0; i < 2; i++ {
		if custody.IsActive() {
			t.Fatal("dead pidfd gained authority for a live replacement")
		}
	}
	custody.active.pid = originalPID
}

func TestLinuxIdentityPollFailureAndCloseFailClosed(t *testing.T) {
	p := &linuxCompanionProcessIdentity{pidfd: 1 << 30}
	if dead, err := p.exited(); !dead && err == nil {
		t.Fatal("invalid descriptor looked alive")
	}
	identity, err := newCompanionProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	linux := identity.(*linuxCompanionProcessIdentity)
	flags, err := unix.FcntlInt(uintptr(linux.pidfd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("pidfd leaks across exec", err)
	}
	identity.close()
	if dead, err := identity.exited(); !dead && err == nil {
		t.Fatal("closed identity looked alive")
	}
}

func TestProcInspectionMissingFilesFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fd"), 0700); err != nil {
		t.Fatal(err)
	}
	local := netip.MustParseAddrPort("127.0.0.1:47002")
	remote := netip.MustParseAddrPort("127.0.0.1:52111")
	if owned, err := inspectCompanionProc(context.Background(), root, local, remote); owned || err == nil {
		t.Fatal("missing TCP table accepted", err)
	}
}
