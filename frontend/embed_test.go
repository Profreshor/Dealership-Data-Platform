package frontend

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
)

func TestHandlerCachePolicy(t *testing.T) {
	var js, css string
	_ = fs.WalkDir(mustDist(t), "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !hashedAsset.MatchString(name) {
			return nil
		}
		switch path.Ext(name) {
		case ".js":
			js = name
		case ".css":
			css = name
		}
		return nil
	})
	if js == "" || css == "" {
		t.Fatalf("embedded build has no hashed js and css assets")
	}

	server := httptest.NewServer(Handler())
	defer server.Close()
	for _, test := range []struct {
		path, cache string
		status      int
	}{
		{"/", "no-cache", http.StatusOK},
		{"/system", "no-cache", http.StatusOK},
		{"/index.html", "no-cache", http.StatusMovedPermanently},
		{"/" + js, "public, max-age=31536000, immutable", http.StatusOK},
		{"/" + css, "public, max-age=31536000, immutable", http.StatusOK},
		{"/assets/missing-12345678.js", "no-cache", http.StatusNotFound},
		{"/assets", "no-cache", http.StatusNotFound},
	} {
		client := *http.DefaultClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != test.status || resp.Header.Get("Cache-Control") != test.cache {
			t.Errorf("GET %s: status=%d cache=%q, want status=%d cache=%q", test.path, resp.StatusCode, resp.Header.Get("Cache-Control"), test.status, test.cache)
		}
	}

	resp, err := http.Head(server.URL + "/system")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 || resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("HEAD /system: status=%d cache=%q body=%d", resp.StatusCode, resp.Header.Get("Cache-Control"), len(body))
	}
}

func TestHashedAssetPattern(t *testing.T) {
	for _, name := range []string{"assets/chunk-a1B2c3D4.js", "assets/chunk-a1B2-c3D4_e5F6.js"} {
		if !hashedAsset.MatchString(name) {
			t.Errorf("hashedAsset does not match %q", name)
		}
	}
	if hashedAsset.MatchString("assets/missing.js") {
		t.Error("hashedAsset matches an unversioned asset")
	}
}

func TestHandlerRejectsUnsupportedMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST /: status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func mustDist(t *testing.T) fs.FS {
	t.Helper()
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
