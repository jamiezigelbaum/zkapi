//go:build linux && amd64

package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestCtlSeqpacketRoundTripPassesCloexecDescriptor(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	defer nsFile.Close()
	host, ns := newCtl(hostFile), newCtl(nsFile)

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	wantMsg := map[string]any{"t": "api", "bridge": "wallet-api"}
	if err := host.send(wantMsg, readEnd); err != nil {
		t.Fatal(err)
	}
	gotMsg, gotFile, err := ns.recv()
	if err != nil {
		t.Fatal(err)
	}
	if gotFile == nil {
		t.Fatal("SCM_RIGHTS descriptor was not received")
	}
	defer gotFile.Close()
	if !reflect.DeepEqual(gotMsg, wantMsg) {
		t.Fatalf("recv message = %#v; want %#v", gotMsg, wantMsg)
	}

	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, gotFile.Fd(), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		t.Fatalf("fcntl(F_GETFD): %v", errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("received descriptor is not close-on-exec")
	}

	const payload = "descriptor-data"
	if _, err := writeEnd.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := gotFile.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("received descriptor read %q; want %q", buf, payload)
	}
}

func TestCtlRecvRejectsTruncatedPacket(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	defer nsFile.Close()

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writeEnd.Close()
	payload := make([]byte, msgMax+1)
	for i := range payload {
		payload[i] = 'x'
	}
	if err := syscall.Sendmsg(int(hostFile.Fd()), payload, syscall.UnixRights(int(readEnd.Fd())), nil, 0); err != nil {
		readEnd.Close()
		t.Fatal(err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, file, err := newCtl(nsFile).recv(); err == nil {
		if file != nil {
			file.Close()
		}
		t.Fatal("oversized seqpacket was accepted")
	} else if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("oversized seqpacket error = %v; want truncation", err)
	}
	assertPassedReadEndClosed(t, writeEnd)
}

func TestCtlRecvRejectsInvalidJSON(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	defer nsFile.Close()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writeEnd.Close()
	if err := syscall.Sendmsg(int(hostFile.Fd()), []byte("not-json"), syscall.UnixRights(int(readEnd.Fd())), nil, 0); err != nil {
		readEnd.Close()
		t.Fatal(err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, file, err := newCtl(nsFile).recv(); err == nil {
		if file != nil {
			file.Close()
		}
		t.Fatal("invalid JSON packet was accepted")
	} else {
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("invalid JSON error = %T %v; want json.SyntaxError", err, err)
		}
	}
	assertPassedReadEndClosed(t, writeEnd)
}

func TestCtlRecvReportsPeerClose(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer nsFile.Close()
	if err := hostFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, file, err := newCtl(nsFile).recv(); !errors.Is(err, errCtlClosed) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("recv after peer close = %v; want %v", err, errCtlClosed)
	}
}

func TestReleaseStage1HardensBeforeGo(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	defer nsFile.Close()

	hardened := false
	stopped := false
	layers, err := releaseStage1(newCtl(hostFile), func() ([]string, error) {
		assertNoCtlPacket(t, nsFile)
		hardened = true
		return []string{"landlock", "seccomp"}, nil
	}, func() { stopped = true })
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Fatal("successful release stopped stage1")
	}
	if !hardened || !reflect.DeepEqual(layers, []string{"landlock", "seccomp"}) {
		t.Fatalf("release returned hardened=%v layers=%q", hardened, layers)
	}
	msg, file, err := newCtl(nsFile).recv()
	if err != nil {
		t.Fatal(err)
	}
	if file != nil {
		file.Close()
		t.Fatal("go message unexpectedly carried a descriptor")
	}
	if msg["t"] != "go" {
		t.Fatalf("release message = %#v; want go", msg)
	}
}

func TestReleaseStage1StopsOnHardenFailureWithoutGo(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	defer nsFile.Close()

	wantErr := errors.New("harden failed")
	stopCalls := 0
	layers, err := releaseStage1(newCtl(hostFile), func() ([]string, error) {
		return nil, wantErr
	}, func() { stopCalls++ })
	if !errors.Is(err, wantErr) || layers != nil {
		t.Fatalf("releaseStage1() = %q, %v; want nil, %v", layers, err, wantErr)
	}
	if stopCalls != 1 {
		t.Fatalf("stop called %d times; want 1", stopCalls)
	}
	assertNoCtlPacket(t, nsFile)
}

func TestReleaseStage1StopsOnGoSendFailure(t *testing.T) {
	hostFile, nsFile, err := newCtlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer hostFile.Close()
	if err := nsFile.Close(); err != nil {
		t.Fatal(err)
	}

	stopCalls := 0
	layers, err := releaseStage1(newCtl(hostFile), func() ([]string, error) {
		return []string{"landlock"}, nil
	}, func() { stopCalls++ })
	if layers != nil || err == nil {
		t.Fatalf("releaseStage1() = %q, %v; want nil, send error", layers, err)
	}
	var sendErr goSendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("release error type = %T; want goSendError", err)
	}
	if stopCalls != 1 {
		t.Fatalf("stop called %d times; want 1", stopCalls)
	}
}

func TestAwaitGoAcceptsOnlyGo(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		hostFile, nsFile, err := newCtlPair()
		if err != nil {
			t.Fatal(err)
		}
		defer hostFile.Close()
		defer nsFile.Close()
		if err := newCtl(hostFile).send(map[string]any{"t": "go"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := awaitGo(newCtl(nsFile)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("other message", func(t *testing.T) {
		hostFile, nsFile, err := newCtlPair()
		if err != nil {
			t.Fatal(err)
		}
		defer hostFile.Close()
		defer nsFile.Close()
		if err := newCtl(hostFile).send(map[string]any{"t": "event"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := awaitGo(newCtl(nsFile)); err == nil {
			t.Fatal("awaitGo accepted a non-go message")
		}
	})

	t.Run("closed", func(t *testing.T) {
		hostFile, nsFile, err := newCtlPair()
		if err != nil {
			t.Fatal(err)
		}
		defer nsFile.Close()
		if err := hostFile.Close(); err != nil {
			t.Fatal(err)
		}
		if err := awaitGo(newCtl(nsFile)); !errors.Is(err, errCtlClosed) {
			t.Fatalf("awaitGo after close = %v; want %v", err, errCtlClosed)
		}
	})
}

func assertNoCtlPacket(t *testing.T, f *os.File) {
	t.Helper()
	p := make([]byte, msgMax)
	oob := make([]byte, syscall.CmsgSpace(4))
	_, _, _, _, err := syscall.Recvmsg(int(f.Fd()), p, oob, syscall.MSG_DONTWAIT)
	if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("control channel had a packet before release: %v", err)
	}
}

func assertPassedReadEndClosed(t *testing.T, writeEnd *os.File) {
	t.Helper()
	if _, err := writeEnd.Write([]byte{1}); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("rejected control packet leaked its passed descriptor: write error = %v; want EPIPE", err)
	}
}
