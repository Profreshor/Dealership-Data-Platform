package doctor

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestLocalAddressRejectsRoutableIP(t *testing.T) {
	if _, _, _, ok := localAddress("192.0.2.1:8080"); ok {
		t.Fatal("localAddress accepted a non-loopback address")
	}
}

func TestPortRejectsRedirectAndOversizedReadyz(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hand  http.HandlerFunc
		state string
	}{
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, "failing"},
		{"oversized", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 4097)) }, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			server := &http.Server{Handler: tc.hand}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			if got := Port(context.Background(), listener.Addr().String()); got.State != tc.state {
				t.Fatalf("Port state = %q, want %q (%s)", got.State, tc.state, got.Message)
			}
		})
	}
}
