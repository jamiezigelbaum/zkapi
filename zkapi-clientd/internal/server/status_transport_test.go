package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActiveStatusReportsTransportEndpointsOnly(t *testing.T) {
	api, err := New(&fakeBackend{}, testKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	api.Status = ServiceStatus{Backend: "zkapi", Network: "mainnet", Transport: &TransportStatus{Kind: "socks5", RelayEndpoint: "127.0.0.1:9150", Companion: "managed", WalletAPI: "127.0.0.1:47002", ConnectProxy: "127.0.0.1:47001"}}
	r := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var got struct {
		Transport map[string]string `json:"transport"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatal("status unavailable", w.Body.String())
	}
	want := map[string]string{"kind": "socks5", "relay_endpoint": "127.0.0.1:9150", "companion": "managed", "wallet_api": "127.0.0.1:47002", "connect_proxy": "127.0.0.1:47001"}
	if len(got.Transport) != len(want) {
		t.Fatal("unexpected transport fields", w.Body.String())
	}
	for key, value := range want {
		if got.Transport[key] != value {
			t.Fatalf("%s = %q, want %q", key, got.Transport[key], value)
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("transport status served without the API bearer")
	}
}
