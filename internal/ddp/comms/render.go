package comms

import (
	"bytes"
	"errors"
	htmltemplate "html/template"
	"io"
	"os"
	"strings"
	texttemplate "text/template"
	"unicode"
)

const (
	maxTemplateInput = 256 << 10
	maxRendered      = 1 << 20
	maxSubject       = 200
)

var (
	errTemplateNotFound = errors.New("comms: template file not found")
	errTemplateInput    = errors.New("comms: template input too large")
	errTemplateInvalid  = errors.New("comms: invalid template")
	errRenderFailed     = errors.New("comms: template rendering failed")
	errOutputTooLarge   = errors.New("comms: rendered output too large")
	errSubjectInvalid   = errors.New("comms: invalid subject")
	errPlainEmpty       = errors.New("comms: plaintext is empty")
)

// Render renders templates/comms/<name>.txt and its optional .html companion.
// The text template must define "subject" and "body"; the HTML template, when
// present, is executed as its root template.
func Render(root, name string, data map[string]any) (Rendered, error) {
	if !validTemplate(name) {
		return Rendered{}, errTemplateInvalid
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return Rendered{}, errTemplateNotFound
	}
	defer r.Close()

	textSource, err := readTemplate(r, "templates/comms/"+name+".txt")
	if err != nil {
		return Rendered{}, err
	}
	plain, err := texttemplate.New(name).Option("missingkey=error").Parse(string(textSource))
	if err != nil || plain.Lookup("subject") == nil || plain.Lookup("body") == nil {
		return Rendered{}, errTemplateInvalid
	}

	total := 0
	subject, err := executeLimited(plain.Lookup("subject"), data, &total, maxSubject+1)
	if err != nil {
		if errors.Is(err, errOutputTooLarge) {
			return Rendered{}, errSubjectInvalid
		}
		return Rendered{}, errRenderFailed
	}
	if len(subject) == 0 || len(subject) > maxSubject || strings.ContainsAny(subject, "\r\n") || hasControl(subject) {
		return Rendered{}, errSubjectInvalid
	}

	textBody, err := executeLimited(plain.Lookup("body"), data, &total, maxRendered)
	if err != nil {
		if errors.Is(err, errOutputTooLarge) {
			return Rendered{}, errOutputTooLarge
		}
		return Rendered{}, errRenderFailed
	}
	if strings.TrimSpace(textBody) == "" {
		return Rendered{}, errPlainEmpty
	}

	htmlBody := ""
	htmlSource, htmlErr := readTemplate(r, "templates/comms/"+name+".html")
	if htmlErr == nil {
		htmlT, parseErr := htmltemplate.New(name).Option("missingkey=error").Parse(string(htmlSource))
		if parseErr != nil {
			return Rendered{}, errTemplateInvalid
		}
		htmlBody, err = executeLimited(htmlT, data, &total, maxRendered)
		if err != nil {
			if errors.Is(err, errOutputTooLarge) {
				return Rendered{}, errOutputTooLarge
			}
			return Rendered{}, errRenderFailed
		}
	} else if !errors.Is(htmlErr, errTemplateNotFound) {
		return Rendered{}, htmlErr
	}
	return Rendered{Subject: subject, Text: textBody, HTML: htmlBody}, nil
}

func readTemplate(root *os.Root, path string) ([]byte, error) {
	info, err := root.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errTemplateNotFound
		}
		return nil, errTemplateInvalid
	}
	if !info.Mode().IsRegular() {
		return nil, errTemplateInvalid
	}
	f, err := root.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errTemplateNotFound
		}
		return nil, errTemplateInvalid
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTemplateInput+1))
	if err != nil {
		return nil, errTemplateInvalid
	}
	if len(b) > maxTemplateInput {
		return nil, errTemplateInput
	}
	return b, nil
}

func executeLimited(t interface{ Execute(io.Writer, any) error }, data any, total *int, limit int) (string, error) {
	remaining := maxRendered - *total
	if limit < remaining {
		remaining = limit
	}
	var b bytes.Buffer
	if err := t.Execute(&limitedWriter{dst: &b, left: remaining}, data); err != nil {
		return "", err
	}
	*total += b.Len()
	return b.String(), nil
}

type limitedWriter struct {
	dst  io.Writer
	left int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		if w.left > 0 {
			_, _ = w.dst.Write(p[:w.left])

		}
		return w.left, errOutputTooLarge
	}
	w.left -= len(p)
	return w.dst.Write(p)
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
