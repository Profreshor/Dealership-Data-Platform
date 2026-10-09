package endpoints

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestWriteCSVTextValuesAndLimit(t *testing.T) {
	conn := csvTestConn(t)
	defer conn.Close(t.Context())

	rows, err := conn.Query(t.Context(), `
		SELECT * FROM (VALUES
			(NULL::text, '12345678901234567890.1200'::numeric, E'  =SUM(A1)\nnext'::text, E'line1\nline2'::text, decode('00ff', 'hex')::bytea),
			('second'::text, '0.00'::numeric, '+2'::text, 'plain'::text, decode('ab', 'hex')::bytea)
		) AS values(nullable, amount, formula, multiline, bytes)`,
		pgx.QueryResultFormats{pgx.TextFormatCode},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out strings.Builder
	count, truncated, err := WriteCSV(&out, rows, []string{"nullable", "amount", "formula", "multiline", "bytes"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !truncated {
		t.Fatalf("count=%d truncated=%t, want 1 true", count, truncated)
	}
	want := "nullable,amount,formula,multiline,bytes\n,12345678901234567890.1200,\"'  =SUM(A1)\nnext\",\"line1\nline2\",\\x00ff\n"
	if out.String() != want {
		t.Fatalf("CSV mismatch\n got: %q\nwant: %q", out.String(), want)
	}
}

func TestWriteCSVPropagatesWriterError(t *testing.T) {
	conn := csvTestConn(t)
	defer conn.Close(t.Context())
	rows, err := conn.Query(t.Context(), "SELECT 'value'::text", pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	w := failingCSVWriter{err: errors.New("writer failed")}
	_, _, err = WriteCSV(&w, rows, []string{"value"}, 1)
	if !errors.Is(err, w.err) {
		t.Fatalf("error=%v, want %v", err, w.err)
	}
}

func TestWriteCSVExactLimitIsNotTruncated(t *testing.T) {
	conn := csvTestConn(t)
	defer conn.Close(t.Context())
	rows, err := conn.Query(t.Context(), "SELECT 'value'::text", pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out strings.Builder
	count, truncated, err := WriteCSV(&out, rows, []string{"value"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || truncated {
		t.Fatalf("count=%d truncated=%t, want 1 false", count, truncated)
	}
}

func TestWriteCSVStopsOnLargeOutputWriterError(t *testing.T) {
	conn := csvTestConn(t)
	defer conn.Close(t.Context())
	rows, err := conn.Query(t.Context(), "SELECT repeat('x', 1000)::text FROM generate_series(1, 20)", pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	w := &failAfterCSVWriter{remaining: 4096, err: errors.New("writer full")}
	count, _, err := WriteCSV(w, rows, []string{"value"}, 20)
	if !errors.Is(err, w.err) {
		t.Fatalf("error=%v, want %v", err, w.err)
	}
	if count >= 20 {
		t.Fatalf("count=%d, want early stop before all rows", count)
	}
	if !rows.Next() {
		t.Fatalf("rows were fully consumed after writer failure; count=%d", count)
	}
}

func TestWriteCSVReturnsRowsCancellation(t *testing.T) {
	conn := csvTestConn(t)
	defer conn.Close(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	rows, err := conn.Query(ctx, "SELECT i, pg_sleep(0.001) FROM generate_series(1, 100000) AS i", pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cancel()

	_, _, err = WriteCSV(io.Discard, rows, []string{"i", "sleep"}, 100000)
	if err == nil {
		t.Fatal("WriteCSV returned nil after query context cancellation")
	}
}

func TestCSVCellNeutralizesSpreadsheetPrefixes(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{"=formula", "'=formula"},
		{"+formula", "'+formula"},
		{"-formula", "'-formula"},
		{"@formula", "'@formula"},
		{"\tvalue", "'\tvalue"},
		{"\rvalue", "'\rvalue"},
		{"\nvalue", "'\nvalue"},
		{"  =formula", "'  =formula"},
		{"plain", "plain"},
	} {
		if got := csvCell(test.input); got != test.want {
			t.Errorf("csvCell(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

type failingCSVWriter struct{ err error }

func (w *failingCSVWriter) Write([]byte) (int, error) { return 0, w.err }

type failAfterCSVWriter struct {
	remaining int
	err       error
}

func (w *failAfterCSVWriter) Write(p []byte) (int, error) {
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.err
}

func csvTestConn(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}
	return conn
}
