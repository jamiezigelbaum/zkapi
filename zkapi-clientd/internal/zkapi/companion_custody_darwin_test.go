//go:build darwin

package zkapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
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
	// Repeat after the first kqueue read to prove the observed exit remains
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

func TestLsofSocketParserRequiresPIDAndExactDirection(t *testing.T) {
	local := netip.MustParseAddrPort("127.0.0.1:47002")
	remote := netip.MustParseAddrPort("127.0.0.1:52111")
	matching := []byte("p123\x00n127.0.0.1:47002->127.0.0.1:52111\x00TST=ESTABLISHED\x00\n")
	if !lsofHasSocket(matching, 123, local, remote) {
		t.Fatal("exact reverse tuple was not recognized")
	}
	for _, output := range [][]byte{
		[]byte("p124\x00n127.0.0.1:47002->127.0.0.1:52111\x00"),
		[]byte("p123\x00n127.0.0.1:47003->127.0.0.1:52111\x00"),
		[]byte("p123\x00n127.0.0.1:52111->127.0.0.1:47002\x00"),
	} {
		if lsofHasSocket(output, 123, local, remote) {
			t.Fatal("nonmatching lsof record was accepted")
		}
	}
}
