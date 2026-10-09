package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data"`
	Error *Error `json:"error"`
}

type Page struct {
	Rows       []map[string]any `json:"rows"`
	NextCursor *string          `json:"next_cursor"`
}

func Write(w http.ResponseWriter, status int, data any) {
	body, err := json.Marshal(Envelope{OK: status < 400, Data: data})
	if err != nil {
		Fail(w, 500, "invalid_response", "Response could not be encoded")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
func Fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Envelope{Error: &Error{Code: code, Message: message}})
}
func Decode(r *http.Request, dst any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return &Error{Code: "invalid_json", Message: "JSON body required"}
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxBody {
		return &Error{Code: "invalid_json", Message: "JSON body exceeds 1 MiB"}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return &Error{Code: "invalid_json", Message: "One JSON value required"}
	}
	return nil
}
