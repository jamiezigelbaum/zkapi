package main

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/zkapi/zkapi-clientd/internal/config"
	"github.com/ethereum/zkapi/zkapi-clientd/internal/zkapi"
)

func TestServeSupervisorOptionsChangeOnlyTheInMemoryRun(t *testing.T) {
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	saved := c
	args := []string{"--relay-url", "socks5://127.0.0.1:9150", "--companion-proxy-listen", "127.0.0.1:47001", "--wallet-api-listen", "127.0.0.1:47002", "--require-managed-companion"}
	if zkapi.CompanionCustodySupported() {
		args = append(args, "--require-companion-custody")
	}
	got, err := serveConfig(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayURL != "socks5://127.0.0.1:9150" || got.CompanionProxyListen != "127.0.0.1:47001" || got.ZKAPI.ClientURL != "http://127.0.0.1:47002" {
		t.Fatalf("supervisor options not applied: %+v", got)
	}
	if got.RequireCompanionCustody != zkapi.CompanionCustodySupported() {
		t.Fatal("custody option was not applied only on supported platforms")
	}
	if got.ZKAPI.BridgeToken != saved.ZKAPI.BridgeToken || got.APIKey != saved.APIKey || got.ZKAPI.Network != saved.ZKAPI.Network {
		t.Fatal("supervisor options changed credentials or network")
	}
	if !reflect.DeepEqual(c, saved) {
		t.Fatal("supervisor options mutated the saved snapshot")
	}
	for _, args := range [][]string{
		{"--relay-url", "socks5://203.0.113.1:9050"},
		{"--relay-url", "http://127.0.0.1:9050"},
		{"--relay-url", ""},
		{"--companion-proxy-listen", "127.0.0.1:0"},
		{"--companion-proxy-listen", "0.0.0.0:47001"},
		{"--companion-proxy-listen", "localhost:47001"},
		{"--wallet-api-listen", "127.0.0.2:47002"},
		{"--wallet-api-listen", ""},
	} {
		if _, err := serveConfig(c, args); err == nil {
			t.Fatalf("accepted invalid supervisor options %q", args)
		}
	}
}

func TestRequireManagedCompanionRefusesExternalCompanion(t *testing.T) {
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	c.ZKAPI.ExternalCompanion = true
	if _, err := serveConfig(c, nil); err != nil {
		t.Fatal("default external companion behavior changed:", err)
	}
	for _, args := range [][]string{{"--require-managed-companion"}, {"--require-companion-custody"}, {"--companion-proxy-listen", "127.0.0.1:47001"}, {"--wallet-api-listen", "127.0.0.1:47002"}} {
		if _, err := serveConfig(c, args); err == nil {
			t.Fatalf("external companion accepted %q", args)
		}
	}
}

func TestCompanionCustodyOptionRefusesUnsupportedPlatforms(t *testing.T) {
	if zkapi.CompanionCustodySupported() {
		t.Skip("custody is supported by this build")
	}
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serveConfig(c, []string{"--require-companion-custody"}); err == nil {
		t.Fatal("unsupported platform accepted companion custody")
	}
}

func TestCompanionCustodyDoesNotProbeOccupiedDaemonPort(t *testing.T) {
	if !zkapi.CompanionCustodySupported() {
		t.Skip("custody is not supported by this build")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	read := make(chan int, 1)
	go func() {
		if tcp, ok := listener.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Now().Add(2 * time.Second))
		}
		connection, err := listener.Accept()
		if err != nil {
			read <- 0
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		data := make([]byte, 4096)
		n, _ := connection.Read(data)
		read <- n
	}()
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	c.Listen = listener.Addr().String()
	dir := filepath.Join(t.TempDir(), "config")
	if err := config.Init(dir, c); err != nil {
		t.Fatal(err)
	}
	expected, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime := expected
	runtime.RequireCompanionCustody = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runConfiguredServe(ctx, dir, runtime, expected, io.Discard); err == nil {
		t.Fatal("custody run accepted an occupied daemon port")
	}
	if n := <-read; n != 0 {
		t.Fatalf("daemon-port occupant received %d credential-bearing bytes", n)
	}
}
