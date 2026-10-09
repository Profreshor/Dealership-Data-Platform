package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func floodRequest(method, path, remote string) *http.Request {
	r := httptest.NewRequest(method, "http://example.test"+path, nil)
	r.RemoteAddr = remote
	return r
}

func TestFloodWindowIdentityAndExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	g := NewFloodGuard()
	g.now = func() time.Time { return now }
	called := 0
	h := g.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	for i := 0; i < floodLimit; i++ {
		h.ServeHTTP(httptest.NewRecorder(), floodRequest("POST", "/api/auth/login", "[::ffff:192.0.2.1]:1"))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, floodRequest("POST", "/api/auth/login", "192.0.2.1:2"))
	if rr.Code != http.StatusTooManyRequests || called != floodLimit {
		t.Fatalf("normalization/rate: status=%d called=%d", rr.Code, called)
	}
	now = now.Add(floodWindow)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, floodRequest("POST", "/api/auth/login", "192.0.2.1:2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("expired window status=%d", rr.Code)
	}
	// Forwarding headers never influence identity.
	r := floodRequest("POST", "/api/auth/login", "198.51.100.1:3")
	r.Header.Set("X-Forwarded-For", "192.0.2.1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(g.peers) != 2 {
		t.Fatalf("spoofed forwarding header changed identity: %d peers", len(g.peers))
	}
}

func TestFloodCapacityAndUnknownBucket(t *testing.T) {
	g := NewFloodGuard()
	g.now = func() time.Time { return time.Unix(1, 0) }
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for i := 0; i < floodMaxPeers; i++ {
		r := floodRequest("POST", "/api/auth/login", "10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256)+":1")
		g.Wrap(next).ServeHTTP(httptest.NewRecorder(), r)
	}
	if len(g.peers) != floodMaxPeers {
		t.Fatalf("peers=%d", len(g.peers))
	}
	rr := httptest.NewRecorder()
	g.Wrap(next).ServeHTTP(rr, floodRequest("POST", "/api/auth/login", "not-an-address"))
	if rr.Code != http.StatusTooManyRequests || len(g.peers) != floodMaxPeers {
		t.Fatalf("capacity status=%d peers=%d", rr.Code, len(g.peers))
	}
	g.now = func() time.Time { return time.Unix(1, 0).Add(floodWindow) }
	rr = httptest.NewRecorder()
	g.Wrap(next).ServeHTTP(rr, floodRequest("POST", "/api/auth/login", "not-an-address"))
	if rr.Code != http.StatusOK || len(g.peers) != 1 {
		t.Fatalf("expiry cleanup status=%d peers=%d", rr.Code, len(g.peers))
	}
}

func TestFloodLeavesSafeAndNonAuthAlone(t *testing.T) {
	called := 0
	h := NewFloodGuard().Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))
	for _, r := range []*http.Request{
		floodRequest("GET", "/api/auth/login", "bad"),
		floodRequest("POST", "/api/data", "bad"),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d", rr.Code)
		}
	}
	if called != 2 {
		t.Fatalf("called=%d", called)
	}
}

func TestFloodConcurrentLimitAndPanicRelease(t *testing.T) {
	g := NewFloodGuard()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h := g.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	var wg sync.WaitGroup
	for i := 0; i < cap(g.slots); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), floodRequest("POST", "/api/auth/login", "192.0.2."+strconv.Itoa(i+1)+":1"))
		}(i)
	}
	for i := 0; i < cap(g.slots); i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("auth handler did not start")
		}
	}
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rr, floodRequest("POST", "/api/auth/login", "192.0.2.99:1"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		unblock()
		<-done
		wg.Wait()
		t.Fatal("excess auth request entered blocked handler")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent status=%d", rr.Code)
	}
	unblock()
	wg.Wait()
	g2 := NewFloodGuard()
	panicHandler := g2.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("test") }))
	func() {
		defer func() { _ = recover() }()
		panicHandler.ServeHTTP(httptest.NewRecorder(), floodRequest("POST", "/api/auth/login", "192.0.2.1:1"))
	}()
	if got := len(g2.slots); got != 0 {
		t.Fatalf("slot leaked after panic: %d", got)
	}
}

func TestFloodReleasesSlotAfterCancellation(t *testing.T) {
	g := NewFloodGuard()
	entered := make(chan struct{})
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := g.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), floodRequest("POST", "/api/auth/login", "192.0.2.1:1").WithContext(ctx))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not stop")
	}
	if len(g.slots) != 0 {
		t.Fatal("slot retained after cancellation")
	}
}
