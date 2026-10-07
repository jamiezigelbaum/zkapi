package relay

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestConnectProxyBindsRequestedAddressWithoutFallback(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	proxy, err := StartConnectProxyOn(context.Background(), "", address)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if proxy.Addr != address || !strings.HasSuffix(proxy.URL, "@"+address) {
		t.Fatalf("proxy bound %s, want %s", proxy.Addr, address)
	}
	if strings.Contains(proxy.Addr, "oa:") {
		t.Fatal("reported address contains the proxy credential")
	}
	// The address is now occupied: a second proxy must fail, not pick another port.
	if second, err := StartConnectProxyOn(context.Background(), "", address); err == nil {
		second.Close()
		t.Fatal("occupied proxy address fell back to another port")
	}
}

func TestDescribeOmitsRelayCredentials(t *testing.T) {
	for _, test := range []struct{ url, kind, endpoint string }{
		{"", "direct", ""},
		{"socks5://127.0.0.1:9150", "socks5", "127.0.0.1:9150"},
		{DefaultURL, "wisp", "wss://oa-1.refraction.network"},
	} {
		kind, endpoint := Describe(test.url)
		if kind != test.kind || endpoint != test.endpoint || strings.Contains(endpoint, "secret") {
			t.Fatalf("Describe(%q) = %q %q", test.url, kind, endpoint)
		}
	}
}
