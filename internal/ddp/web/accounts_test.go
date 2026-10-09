package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountHandlersRejectInvalidRequestsBeforeAuth(t *testing.T) {
	g := &Guard{Origin: "https://app.example"}
	for name, handler := range map[string]http.HandlerFunc{
		"invite":        g.Invite(nil),
		"reset request": g.RequestReset(nil),
		"set password":  g.SetPassword(nil),
	} {
		t.Run(name+" origin", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://evil.example")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
		t.Run(name+" json", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://app.example")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestWaitMinimumHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitMinimum(ctx, time.Now()) {
		t.Fatal("waitMinimum accepted a cancelled context")
	}
}
