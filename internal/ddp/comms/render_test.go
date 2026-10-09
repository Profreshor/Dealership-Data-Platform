package comms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemplates(t *testing.T, txt, html string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "templates", "comms")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte(txt), 0o600); err != nil {
		t.Fatal(err)
	}
	if html != "" {
		if err := os.WriteFile(filepath.Join(dir, "x.html"), []byte(html), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRender(t *testing.T) {
	t.Run("default templates", func(t *testing.T) {
		data := map[string]any{
			"Severity": "warning", "Title": "Freshness", "Message": "Late", "OccurredAt": "today", "Details": "none",
			"Date": "today", "Items": []map[string]any{{"Title": "Job", "Status": "ok", "URL": "https://example.test/job"}},
			"Name": "Ada", "ResetURL": "https://example.test/reset", "ExpiresIn": "one hour", "LoginURL": "https://example.test/login",
		}
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"alert", "digest", "password-reset", "welcome"} {
			if r, err := Render(root, name, data); err != nil || r.Text == "" || r.HTML == "" || r.Subject == "" {
				t.Fatalf("%s: %#v, %v", name, r, err)
			}
		}
	})
	t.Run("renders and escapes html", func(t *testing.T) {
		root := writeTemplates(t, `{{define "subject"}}Hi {{.Name}}{{end}}{{define "body"}}Hello {{.Name}}{{end}}`, `<p>{{.Name}}</p>`)
		r, err := Render(root, "x", map[string]any{"Name": `<script>alert(1)</script>`})
		if err != nil || r.Text != "Hello <script>alert(1)</script>" || strings.Contains(r.HTML, "<script>alert") {
			t.Fatalf("render = %#v, err = %v", r, err)
		}
	})
	t.Run("missing context", func(t *testing.T) {
		root := writeTemplates(t, `{{define "subject"}}{{.Name}}{{end}}{{define "body"}}{{.Missing}}{{end}}`, "")
		if _, err := Render(root, "x", map[string]any{"Name": "ok"}); err != errRenderFailed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("subject injection and plaintext", func(t *testing.T) {
		root := writeTemplates(t, `{{define "subject"}}{{.Subject}}{{end}}{{define "body"}}ok{{end}}`, "")
		for _, subject := range []string{"bad\nsubject", strings.Repeat("x", maxSubject+1)} {
			if _, err := Render(root, "x", map[string]any{"Subject": subject}); err != errSubjectInvalid {
				t.Fatalf("subject %q: err = %v", subject, err)
			}
		}
	})
	t.Run("input and output limits", func(t *testing.T) {
		root := writeTemplates(t, `{{define "subject"}}ok{{end}}{{define "body"}}{{.Body}}{{end}}`, "")
		if _, err := Render(root, "x", map[string]any{"Body": strings.Repeat("x", maxRendered)}); err != errOutputTooLarge {
			t.Fatalf("output err = %v", err)
		}
		root = writeTemplates(t, strings.Repeat("x", maxTemplateInput+1), "")
		if _, err := Render(root, "x", nil); err != errTemplateInput {
			t.Fatalf("input err = %v", err)
		}
	})
	t.Run("path and missing file", func(t *testing.T) {
		if _, err := Render(t.TempDir(), "../x", nil); err != errTemplateInvalid {
			t.Fatalf("path err = %v", err)
		}
		if _, err := Render(t.TempDir(), "x", nil); err != errTemplateNotFound {
			t.Fatalf("missing err = %v", err)
		}
	})
	t.Run("escaping symlink", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "templates", "comms")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "x.txt")
		if err := os.WriteFile(outside, []byte(`{{define "subject"}}x{{end}}{{define "body"}}x{{end}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "x.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := Render(root, "x", nil); err == nil {
			t.Fatal("escaping symlink rendered")
		}
	})
}
