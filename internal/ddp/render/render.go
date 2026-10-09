// Package render defines the versioned CLI response used by every command.
package render

import (
	"encoding/json"
	"fmt"
	"io"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Envelope struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Data    any    `json:"data"`
	Error   *Error `json:"error"`
}

func Write(w io.Writer, machine bool, data any, err *Error) error {
	if machine {
		return json.NewEncoder(w).Encode(Envelope{Version: 1, OK: err == nil, Data: data, Error: err})
	}
	if err != nil {
		_, e := fmt.Fprintln(w, err.Message)
		return e
	}
	if value, ok := data.(string); ok {
		_, e := fmt.Fprintln(w, value)
		return e
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}
