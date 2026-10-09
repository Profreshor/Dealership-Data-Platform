package serving

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func TestPortalMetricsArePrivateAndUseRegisteredPatterns(t *testing.T) {
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	portal, err := PortalHandler(nil, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(reg *web.Registry) {
		reg.Handle("GET /api/client/{id}", http.NotFoundHandler(), web.Public())
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/metrics", "/api/secret-customer-123?token=secret-value", "/api/secret-customer-456", "/api/client/secret-a", "/api/client/secret-b"} {
		w := httptest.NewRecorder()
		portal.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("portal path %q: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	portal.Metrics.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `ddp_http_request_duration_seconds_count{method="GET",route="/api/",status="404"} 2`) {
		t.Fatalf("scrape: %d %s", w.Code, body)
	}
	if strings.Contains(body, "secret-") {
		t.Fatal("request data escaped into metrics")
	}
	if !strings.Contains(body, `ddp_http_request_duration_seconds_count{method="GET",route="GET /api/client/{id}",status="404"} 2`) {
		t.Fatal("client route missing from bounded metrics", body)
	}
}
