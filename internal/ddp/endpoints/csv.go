package endpoints

import (
	"encoding/csv"
	"fmt"
	"io"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// WriteCSV writes the declared columns and at most maxRows rows from rows.
// The query should request pgx.QueryResultFormats{pgx.TextFormatCode} so that
// RawValues preserves PostgreSQL's text representation.
func WriteCSV(w io.Writer, rows pgx.Rows, columns []string, maxRows int) (count int, truncated bool, err error) {
	if maxRows < 0 {
		return 0, false, fmt.Errorf("maxRows must be non-negative")
	}

	csvWriter := csv.NewWriter(w)
	header := make([]string, len(columns))
	for i, column := range columns {
		header[i] = csvCell(column)
	}
	if err := csvWriter.Write(header); err != nil {
		return 0, false, err
	}

	for rows.Next() {
		if count == maxRows {
			truncated = true // This row is the one extra row used to detect truncation.
			break
		}

		raw := rows.RawValues()
		if len(raw) != len(columns) {
			return count, false, fmt.Errorf("query returned %d columns, want %d", len(raw), len(columns))
		}
		record := make([]string, len(raw))
		for i, value := range raw {
			if value != nil {
				record[i] = csvCell(string(value))
			}
		}
		if err := csvWriter.Write(record); err != nil {
			return count, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, truncated, err
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		return count, truncated, err
	}
	return count, truncated, nil
}

func csvCell(value string) string {
	for _, r := range value {
		switch r {
		case '=', '+', '-', '@':
			return "'" + value
		case '\t', '\r', '\n':
			return "'" + value
		}
		if !unicode.IsSpace(r) {
			break
		}
	}
	return value
}
