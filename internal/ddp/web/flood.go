package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
)

const (
	floodWindow   = time.Minute
	floodLimit    = 60
	floodMaxPeers = 4096
)

// FloodGuard bounds authentication work per process and per observed peer.
// ponytail: process-local actual peer means proxy/NAT clients share a budget until a verified trusted proxy topology is configured.
type FloodGuard struct {
	mu    sync.Mutex
	peers map[string]*floodBucket
	now   func() time.Time
	slots chan struct{}
}

type floodBucket struct {
	started time.Time
	count   int
}

func NewFloodGuard() *FloodGuard {
	return &FloodGuard{
		peers: make(map[string]*floodBucket),
		now:   time.Now,
		slots: make(chan struct{}, 4),
	}
}

// Wrap limits unsafe requests under /api/auth/ before they reach decoding or authentication.
func (g *FloodGuard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/auth/") || !unsafe(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !g.takePeer(peerKey(r)) {
			w.Header().Set("Retry-After", "60")
			httpx.Fail(w, http.StatusTooManyRequests, "rate_limited", "Too many authentication requests")
			return
		}
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		case <-r.Context().Done():
			return
		default:
			w.Header().Set("Retry-After", "1")
			httpx.Fail(w, http.StatusTooManyRequests, "rate_limited", "Too many authentication requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *FloodGuard) takePeer(key string) bool {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	// ponytail: scan at most 4096 peers per auth request; use timed expiry if this becomes hot.
	for k, b := range g.peers {
		if !now.Before(b.started.Add(floodWindow)) {
			delete(g.peers, k)
		}
	}
	b := g.peers[key]
	if b == nil {
		if len(g.peers) >= floodMaxPeers {
			return false
		}
		b = &floodBucket{started: now}
		g.peers[key] = b
	}
	if b.count >= floodLimit {
		return false
	}
	b.count++
	return true
}

func peerKey(r *http.Request) string {
	host := r.RemoteAddr
	if addr, err := netip.ParseAddr(strings.TrimSpace(host)); err == nil {
		return addr.Unmap().String()
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return "unknown"
	}
	return addr.Unmap().String()
}
