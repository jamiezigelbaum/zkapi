package main

import (
	"reflect"
	"testing"

	"github.com/ethereum/zkapi/zkapi-clientd/internal/config"
)

func TestServeSupervisorOptionsChangeOnlyTheInMemoryRun(t *testing.T) {
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	saved := c
	got, err := serveConfig(c, []string{"--relay-url", "socks5://127.0.0.1:9150", "--companion-proxy-listen", "127.0.0.1:47001", "--wallet-api-listen", "127.0.0.1:47002", "--require-managed-companion"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayURL != "socks5://127.0.0.1:9150" || got.CompanionProxyListen != "127.0.0.1:47001" || got.ZKAPI.ClientURL != "http://127.0.0.1:47002" {
		t.Fatalf("supervisor options not applied: %+v", got)
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
	for _, args := range [][]string{{"--require-managed-companion"}, {"--companion-proxy-listen", "127.0.0.1:47001"}, {"--wallet-api-listen", "127.0.0.1:47002"}} {
		if _, err := serveConfig(c, args); err == nil {
			t.Fatalf("external companion accepted %q", args)
		}
	}
}
