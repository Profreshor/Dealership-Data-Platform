package httpx

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteRejectsUnencodableResponseBeforeHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, 200, map[string]any{"value": math.Inf(1)})
	var response Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 500 || response.Error == nil || response.OK {
		t.Fatalf("invalid JSON reported success: %d %s %v", w.Code, w.Body.String(), err)
	}
}

func TestDecodeAcceptsOneStrictJSONValue(t *testing.T) {
	type input struct {
		Name string `json:"name"`
	}
	for _, tc := range []struct {
		name, contentType, body string
		wantErr                 bool
	}{
		{name: "exact limit", contentType: "application/json", body: `{"name":"Ada"}` + strings.Repeat(" ", (1<<20)-len(`{"name":"Ada"}`))},
		{name: "hidden second value beyond limit", contentType: "application/json", body: `{"name":"Ada"}` + strings.Repeat(" ", (1<<20)-len(`{"name":"Ada"}`)) + `{}`, wantErr: true},
		{name: "oversized value", contentType: "application/json", body: `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, wantErr: true},
		{name: "valid", contentType: "application/json", body: `{"name":"Ada"}`},
		{name: "wrong content type", contentType: "text/plain", body: `{"name":"Ada"}`, wantErr: true},
		{name: "unknown field", contentType: "application/json", body: `{"name":"Ada","admin":true}`, wantErr: true},
		{name: "second value", contentType: "application/json", body: `{"name":"Ada"}{"name":"Grace"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			var got input
			err := Decode(r, &got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got.Name != "Ada" {
				t.Fatalf("Decode() = %#v", got)
			}
		})
	}
}
