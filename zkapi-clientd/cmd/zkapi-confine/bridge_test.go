//go:build linux && amd64

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestServerHandshakeMatchesIndependentMutualHMACProtocol(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	const name = "wallet-api"
	result := make(chan bool, 1)
	go func() { result <- serverHandshake(server, token, name) }()

	clientNonce := bytes.Repeat([]byte{0x5a}, 16)
	serverNonce := readServerProof(t, client, clientNonce, token, name, true)
	if _, err := client.Write(protocolMAC(token, "cli", name, clientNonce, serverNonce)); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("server rejected the independent client proof")
		}
	case <-time.After(time.Second):
		t.Fatal("server handshake did not finish")
	}
}

func TestServerHandshakeRejectsWrongTokenNameAndProof(t *testing.T) {
	serverToken := []byte("0123456789abcdef0123456789abcdef")
	for _, test := range []struct {
		name        string
		clientToken []byte
		clientName  string
		corrupt     bool
	}{
		{name: "token", clientToken: []byte("abcdef0123456789abcdef0123456789"), clientName: "wallet-api"},
		{name: "name", clientToken: serverToken, clientName: "other-api"},
		{name: "proof", clientToken: serverToken, clientName: "wallet-api", corrupt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			result := make(chan bool, 1)
			go func() { result <- serverHandshake(server, serverToken, "wallet-api") }()

			clientNonce := bytes.Repeat([]byte{0x31}, 16)
			serverNonce := readServerProof(t, client, clientNonce, test.clientToken, test.clientName, test.corrupt)
			proof := protocolMAC(test.clientToken, "cli", test.clientName, clientNonce, serverNonce)
			if test.corrupt {
				proof[0] ^= 0xff
			}
			if _, err := client.Write(proof); err != nil {
				t.Fatal(err)
			}
			select {
			case ok := <-result:
				if ok {
					t.Fatal("server accepted an invalid client proof")
				}
			case <-time.After(time.Second):
				t.Fatal("server handshake did not finish")
			}
		})
	}
}

func TestServerHandshakeRejectsShortIO(t *testing.T) {
	token := []byte("0123456789abcdef0123456789abcdef")

	t.Run("nonce", func(t *testing.T) {
		server, client := net.Pipe()
		defer server.Close()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		result := make(chan bool, 1)
		go func() { result <- serverHandshake(server, token, "api") }()
		if _, err := client.Write([]byte("short")); err != nil {
			t.Fatal(err)
		}
		client.Close()
		awaitHandshakeResult(t, result, false)
	})

	t.Run("proof", func(t *testing.T) {
		server, client := net.Pipe()
		defer server.Close()
		result := make(chan bool, 1)
		go func() { result <- serverHandshake(server, token, "api") }()
		clientNonce := bytes.Repeat([]byte{0x22}, 16)
		_ = readServerProof(t, client, clientNonce, token, "api", true)
		if _, err := client.Write([]byte("short-proof")); err != nil {
			t.Fatal(err)
		}
		client.Close()
		awaitHandshakeResult(t, result, false)
	})
}

func TestPumpCarriesBothDirectionsAndPreservesTCPHalfClose(t *testing.T) {
	aPeer, aPump := tcpPair(t)
	bPeer, bPump := tcpPair(t)
	defer aPeer.Close()
	defer bPeer.Close()
	defer aPump.Close()
	defer bPump.Close()
	deadline := time.Now().Add(2 * time.Second)
	_ = aPeer.SetDeadline(deadline)
	_ = bPeer.SetDeadline(deadline)

	done := make(chan struct{})
	go func() {
		pump(aPump, bPump)
		close(done)
	}()

	assertCopied(t, aPeer, bPeer, []byte("request"))
	assertCopied(t, bPeer, aPeer, []byte("response"))

	if err := aPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	assertEOF(t, bPeer)
	assertCopied(t, bPeer, aPeer, []byte("response-after-request-eof"))
	if err := bPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	assertEOF(t, aPeer)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pump did not finish after both TCP half-closes")
	}
}

func TestDialAndPumpClosesSourceWhenDestinationIsDead(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddress := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	sourceFile := os.NewFile(uintptr(fds[0]), "dial-source")
	peerFile := os.NewFile(uintptr(fds[1]), "dial-peer")
	peer, err := net.FileConn(peerFile)
	peerFile.Close()
	if err != nil {
		sourceFile.Close()
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))

	go dialAndPump(sourceFile, deadAddress)
	assertClosedRead(t, peer, "source after dead dial")
}

func TestPumpWithGraceClosesStalledPeerWithinBound(t *testing.T) {
	aPeer, aPump := net.Pipe()
	bPeer, bPump := net.Pipe()
	defer bPeer.Close()
	done := make(chan struct{})
	go func() {
		pumpWithGrace(aPump, bPump, 20*time.Millisecond)
		close(done)
	}()

	if err := aPeer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pumpWithGrace did not close a stalled peer")
	}
}

func TestExposeBridgeLimitsPendingAuthentication(t *testing.T) {
	bridge, token, ctlHost, ctlNS, loopDone := startExposeTestBridge(t)
	defer ctlHost.Close()
	defer ctlNS.Close()
	defer func() {
		bridge.ln.Close()
		<-loopDone
		os.Remove(bridge.path)
	}()

	clients := make([]net.Conn, 0, maxBridgeConns)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for i := 0; i < maxBridgeConns; i++ {
		c, err := net.Dial("unix", bridge.path)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		nonce := bytes.Repeat([]byte{byte(i + 1)}, 16)
		if _, err := c.Write(nonce); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 48)
		if _, err := io.ReadFull(c, reply); err != nil {
			t.Fatalf("pending handshake %d: %v", i, err)
		}
		if !hmac.Equal(reply[16:], protocolMAC(token, "srv", bridge.name, nonce, reply[:16])) {
			t.Fatalf("pending handshake %d received an invalid server proof", i)
		}
	}

	overflow, err := net.Dial("unix", bridge.path)
	if err != nil {
		t.Fatal(err)
	}
	defer overflow.Close()
	_ = overflow.SetDeadline(time.Now().Add(time.Second))
	if _, err := overflow.Write(bytes.Repeat([]byte{0xee}, 16)); err == nil {
		reply := make([]byte, 48)
		n, err := io.ReadFull(overflow, reply)
		if err == nil || n == len(reply) {
			t.Fatal("65th pending authentication was admitted")
		}
		assertNotTimeout(t, err, "65th pending authentication close")
	} else {
		assertNotTimeout(t, err, "65th pending authentication write")
	}
}

func TestExposeBridgeDoesNotCountEstablishedConnectionsAgainstPendingLimit(t *testing.T) {
	bridge, token, ctlHostFile, ctlNSFile, loopDone := startExposeTestBridge(t)
	defer ctlHostFile.Close()
	defer ctlNSFile.Close()
	defer func() {
		bridge.ln.Close()
		<-loopDone
		os.Remove(bridge.path)
	}()
	if err := syscall.SetsockoptTimeval(int(ctlNSFile.Fd()), syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 2}); err != nil {
		t.Fatal(err)
	}
	ctlNS := newCtl(ctlNSFile)

	clients := make([]net.Conn, 0, maxBridgeConns+1)
	passed := make([]*os.File, 0, maxBridgeConns+1)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
		for _, f := range passed {
			f.Close()
		}
	}()
	for i := 0; i < maxBridgeConns+1; i++ {
		c, err := net.Dial("unix", bridge.path)
		if err != nil {
			t.Fatalf("dial established connection %d: %v", i, err)
		}
		clients = append(clients, c)
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		nonce := bytes.Repeat([]byte{byte(i + 1)}, 16)
		serverNonce := readServerProof(t, c, nonce, token, bridge.name, true)
		if _, err := c.Write(protocolMAC(token, "cli", bridge.name, nonce, serverNonce)); err != nil {
			t.Fatal(err)
		}
		msg, f, err := ctlNS.recv()
		if err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
		if msg["t"] != "api" || msg["bridge"] != bridge.name || f == nil {
			if f != nil {
				f.Close()
			}
			t.Fatalf("handoff %d = %#v, %v", i, msg, f)
		}
		passed = append(passed, f)
	}
}

func TestReachBridgeLimitsPendingHandoffs(t *testing.T) {
	base, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, maxBridgeConns+1)
	ln := &acceptSignalListener{Listener: base, accepted: accepted}
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	ctl := newCtl(hostFile)
	ctl.wmu.Lock()
	mutexHeld := true
	loopDone := make(chan struct{})
	go func() {
		reachLoop(ln, 9150, ctl)
		close(loopDone)
	}()

	clients := make([]net.Conn, 0, maxBridgeConns+1)
	defer func() {
		if mutexHeld {
			nsFile.Close()
			ctl.wmu.Unlock()
		}
		for _, c := range clients {
			c.Close()
		}
		base.Close()
		<-loopDone
		hostFile.Close()
	}()
	for i := 0; i < maxBridgeConns+1; i++ {
		c, err := net.Dial("tcp4", base.Addr().String())
		if err != nil {
			t.Fatalf("dial pending handoff %d: %v", i, err)
		}
		clients = append(clients, c)
		select {
		case <-accepted:
		case <-time.After(time.Second):
			t.Fatalf("accept pending handoff %d timed out", i)
		}
	}

	overflow := clients[len(clients)-1]
	_ = overflow.SetReadDeadline(time.Now().Add(time.Second))
	assertClosedRead(t, overflow, "65th pending handoff")
	// Make every blocked send fail before releasing the test-held mutex, so all
	// handoff goroutines can finish without depending on a control receiver.
	if err := nsFile.Close(); err != nil {
		t.Fatal(err)
	}
	ctl.wmu.Unlock()
	mutexHeld = false
	for _, c := range clients[:maxBridgeConns] {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		assertClosedRead(t, c, "released pending handoff")
	}
}

func protocolMAC(token []byte, role, name string, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, token)
	_, _ = io.WriteString(h, "zkapi-confine/")
	_, _ = io.WriteString(h, role)
	_, _ = io.WriteString(h, "/")
	_, _ = io.WriteString(h, name)
	_, _ = h.Write([]byte{0})
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func readServerProof(t *testing.T, c net.Conn, clientNonce, token []byte, name string, wantValid bool) []byte {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(clientNonce); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 16+sha256.Size)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	serverNonce := append([]byte(nil), reply[:16]...)
	valid := hmac.Equal(reply[16:], protocolMAC(token, "srv", name, clientNonce, serverNonce))
	if valid != wantValid {
		t.Fatalf("server proof validity = %v; want %v", valid, wantValid)
	}
	return serverNonce
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan *net.TCPConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := ln.AcceptTCP()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- c
	}()
	client, err := net.DialTCP("tcp4", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case server := <-accepted:
		return client, server
	case err := <-acceptErr:
		client.Close()
		t.Fatal(err)
	case <-time.After(time.Second):
		client.Close()
		t.Fatal("TCP accept did not finish")
	}
	return nil, nil
}

func assertCopied(t *testing.T, src, dst net.Conn, payload []byte) {
	t.Helper()
	if _, err := src.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(dst, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("copied payload = %q; want %q", got, payload)
	}
}

func assertEOF(t *testing.T, c net.Conn) {
	t.Helper()
	buf := make([]byte, 1)
	n, err := c.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read after half-close = %d, %v; want 0, EOF", n, err)
	}
}

func assertClosedRead(t *testing.T, c net.Conn, context string) {
	t.Helper()
	n, err := c.Read(make([]byte, 1))
	if err == nil || n != 0 {
		t.Fatalf("%s remained open: read = %d, %v", context, n, err)
	}
	assertNotTimeout(t, err, context)
}

func assertNotTimeout(t *testing.T, err error, context string) {
	t.Helper()
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("%s timed out instead of closing: %v", context, err)
	}
}

func awaitHandshakeResult(t *testing.T, result <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("server handshake result = %v; want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("server handshake did not finish")
	}
}

func startExposeTestBridge(t *testing.T) (*exposeBridge, []byte, *os.File, *os.File, <-chan struct{}) {
	t.Helper()
	dir, err := os.MkdirTemp("", "zc-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	bridge := &exposeBridge{name: "api", port: 47002, path: path, ln: ln}
	token := []byte("0123456789abcdef0123456789abcdef")
	done := make(chan struct{})
	go func() {
		exposeLoop(bridge, token, newCtl(hostFile))
		close(done)
	}()
	return bridge, token, hostFile, nsFile, done
}

type acceptSignalListener struct {
	net.Listener
	accepted chan<- struct{}
}

func (l *acceptSignalListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return c, err
}
