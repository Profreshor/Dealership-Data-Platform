package endpoints

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
)

// Query is the complete, parameterized query selected by an endpoint request.
type Query struct {
	SQL         string
	Args        []any
	Limit       int
	Order       []string
	Fingerprint string
}

type cursorPayload struct {
	Version     int       `json:"v"`
	Fingerprint string    `json:"f"`
	Values      []*string `json:"values"`
}

type orderTerm struct {
	Column string
	Desc   bool
}

func Plan(endpointRef string, ep config.Endpoint, params url.Values, mode string) (Query, error) {
	if mode != "list" && mode != "get" && mode != "export" {
		return Query{}, fmt.Errorf("unknown query mode %q", mode)
	}
	relation, err := endpointRelation(ep)
	if err != nil {
		return Query{}, err
	}
	if len(ep.Columns) == 0 {
		return Query{}, fmt.Errorf("endpoint columns are required")
	}
	columns := map[string]bool{}
	for _, c := range ep.Columns {
		if c == "" || columns[c] {
			return Query{}, fmt.Errorf("invalid or duplicate column %q", c)
		}
		columns[c] = true
	}
	for _, c := range ep.UniqueKey {
		if !columns[c] {
			return Query{}, fmt.Errorf("unique key %q must be returned", c)
		}
	}
	for key, values := range params {
		if len(values) != 1 {
			return Query{}, fmt.Errorf("parameter %q must occur exactly once", key)
		}
	}
	allowed := map[string]bool{"limit": true, "sort": true, "q": true, "cursor": true}
	for _, f := range ep.Filters {
		allowed["filter."+f] = true
	}
	for _, k := range ep.UniqueKey {
		allowed["key."+k] = true
	}
	for key := range params {
		if !allowed[key] {
			return Query{}, fmt.Errorf("unknown parameter %q", key)
		}
	}

	fingerprint := endpointFingerprint(endpointRef, ep, params)
	if mode == "get" {
		return planGet(ep, relation, params, fingerprint)
	}
	if mode == "export" {
		return planExport(ep, relation, params, fingerprint)
	}
	if ep.Shape == "singleton" {
		if _, ok := params["cursor"]; ok {
			return Query{}, fmt.Errorf("singleton does not support cursor")
		}
		if _, ok := params["sort"]; ok {
			return Query{}, fmt.Errorf("singleton does not support sort")
		}
		if _, ok := params["limit"]; ok {
			return Query{}, fmt.Errorf("singleton does not support limit")
		}
		if len(ep.UniqueKey) == 0 {
			return planList(ep, relation, params, fingerprint, nil, 2)
		}
	}
	if ep.Shape != "singleton" && len(ep.UniqueKey) == 0 {
		return Query{}, fmt.Errorf("paginated endpoint requires unique_key")
	}
	for key := range params {
		if strings.HasPrefix(key, "key.") {
			return Query{}, fmt.Errorf("%s does not accept key parameters", mode)
		}
	}
	limit := ep.PageSize
	if limit == 0 {
		limit = 50
	}
	if raw, ok := params["limit"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 1000 {
			return Query{}, fmt.Errorf("limit must be an integer from 1 to 1000")
		}
		limit = n
	}
	order, err := parseOrder(ep, params.Get("sort"))
	if err != nil {
		return Query{}, err
	}
	var cursor []*string
	if raw, ok := params["cursor"]; ok {
		cursor, err = decodeCursor(raw[0], fingerprint, len(order))
		if err != nil {
			return Query{}, err
		}
	}
	return planList(ep, relation, params, fingerprint, cursor, limit)
}

func endpointRelation(ep config.Endpoint) (string, error) {
	if len(ep.Reads) != 1 {
		return "", fmt.Errorf("endpoint must read exactly one relation")
	}
	parts := strings.Split(ep.Reads[0], "/")
	if len(parts) != 2 || (parts[0] != "model" && parts[0] != "table") || len(strings.Split(parts[1], ".")) != 2 {
		return "", fmt.Errorf("invalid endpoint relation %q", ep.Reads[0])
	}
	return pgx.Identifier(strings.Split(parts[1], ".")).Sanitize(), nil
}

func parseOrder(ep config.Endpoint, raw string) ([]orderTerm, error) {
	defaults := ep.Sort
	terms := defaults
	if raw != "" {
		terms = strings.Split(raw, ",")
	}
	allowed := map[string]bool{}
	for _, s := range ep.Sort {
		allowed[strings.TrimPrefix(s, "-")] = true
	}
	seen := map[string]bool{}
	order := make([]orderTerm, 0, len(terms)+len(ep.UniqueKey))
	for _, term := range terms {
		if term == "" {
			return nil, fmt.Errorf("sort contains an empty term")
		}
		desc := strings.HasPrefix(term, "-")
		col := strings.TrimPrefix(term, "-")
		if !allowed[col] {
			return nil, fmt.Errorf("sort column %q is not allowed", col)
		}
		if seen[col] {
			return nil, fmt.Errorf("sort column %q occurs more than once", col)
		}
		seen[col] = true
		order = append(order, orderTerm{Column: col, Desc: desc})
	}
	for _, col := range ep.UniqueKey {
		if !seen[col] {
			order = append(order, orderTerm{Column: col})
			seen[col] = true
		}
	}
	return order, nil
}

func planList(ep config.Endpoint, relation string, params url.Values, fingerprint string, cursor []*string, limit int) (Query, error) {
	order, err := parseOrder(ep, params.Get("sort"))
	if err != nil {
		return Query{}, err
	}
	args := []any{}
	where, err := filters(ep, params, &args)
	if err != nil {
		return Query{}, err
	}
	if len(cursor) > 0 {
		where = appendWhere(where, keyset(order, cursor, &args))
	}
	selects := make([]string, len(ep.Columns))
	for i, c := range ep.Columns {
		selects[i] = qualified(c)
	}
	for i, o := range order {
		selects = append(selects, qualified(o.Column)+"::text AS "+fmt.Sprintf("__ddp_order_%d", i))
	}
	sql := "SELECT " + strings.Join(selects, ", ") + " FROM " + relation + " AS r"
	if where != "" {
		sql += " WHERE " + where
	}
	if len(order) > 0 {
		sql += " ORDER BY " + orderSQL(order)
	}
	fetch := limit + 1
	if ep.Shape == "singleton" {
		fetch = 2
	}
	sql += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, fetch)
	return Query{SQL: sql, Args: args, Limit: limit, Order: orderNames(order), Fingerprint: fingerprint}, nil
}

func planGet(ep config.Endpoint, relation string, params url.Values, fingerprint string) (Query, error) {
	if len(ep.UniqueKey) == 0 {
		return Query{}, fmt.Errorf("get requires unique_key")
	}
	args := []any{}
	clauses := []string{}
	for _, key := range ep.UniqueKey {
		raw, ok := params["key."+key]
		if !ok {
			return Query{}, fmt.Errorf("missing key.%s", key)
		}
		clauses = append(clauses, qualified(key)+fmt.Sprintf(" = $%d", len(args)+1))
		args = append(args, raw[0])
	}
	for key := range params {
		if !strings.HasPrefix(key, "key.") {
			return Query{}, fmt.Errorf("get accepts key parameters only")
		}
	}
	cols := make([]string, len(ep.Columns))
	for i, c := range ep.Columns {
		cols[i] = qualified(c)
	}
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM " + relation + " AS r WHERE " + strings.Join(clauses, " AND ") + fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, 2)
	return Query{SQL: sql, Args: args, Limit: 2, Fingerprint: fingerprint}, nil
}

func planExport(ep config.Endpoint, relation string, params url.Values, fingerprint string) (Query, error) {
	if ep.Export == nil {
		return Query{}, fmt.Errorf("endpoint has no export")
	}
	if _, ok := params["cursor"]; ok {
		return Query{}, fmt.Errorf("export does not support cursor")
	}
	if _, ok := params["limit"]; ok {
		return Query{}, fmt.Errorf("export does not support limit")
	}
	for key := range params {
		if strings.HasPrefix(key, "key.") {
			return Query{}, fmt.Errorf("export does not accept key parameters")
		}
	}
	max := ep.Export.MaxRows
	if max < 1 {
		return Query{}, fmt.Errorf("export max_rows must be positive")
	}
	args := []any{}
	where, err := filters(ep, params, &args)
	if err != nil {
		return Query{}, err
	}
	order, err := parseOrder(ep, params.Get("sort"))
	if err != nil {
		return Query{}, err
	}
	cols := make([]string, len(ep.Columns))
	for i, c := range ep.Columns {
		cols[i] = qualified(c)
	}
	sql := "SELECT " + strings.Join(cols, ", ") + " FROM " + relation + " AS r"
	if where != "" {
		sql += " WHERE " + where
	}
	if len(order) > 0 {
		sql += " ORDER BY " + orderSQL(order)
	}
	sql += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, max+1)
	return Query{SQL: sql, Args: args, Limit: max, Order: orderNames(order), Fingerprint: fingerprint}, nil
}

func filters(ep config.Endpoint, params url.Values, args *[]any) (string, error) {
	parts := []string{}
	for _, f := range ep.Filters {
		if raw, ok := params["filter."+f]; ok {
			*args = append(*args, raw[0])
			parts = append(parts, qualified(f)+fmt.Sprintf(" = $%d", len(*args)))
		}
	}
	if raw, ok := params["q"]; ok && raw[0] != "" {
		if len(ep.Search) == 0 {
			return "", fmt.Errorf("q is not supported by this endpoint")
		}
		var cols []string
		q := escapeLike(raw[0])
		*args = append(*args, q)
		for _, c := range ep.Search {
			cols = append(cols, qualified(c)+"::text"+fmt.Sprintf(" ILIKE '%%' || $%d || '%%' ESCAPE E'\\\\'", len(*args)))
		}
		if len(cols) > 0 {
			parts = append(parts, "("+strings.Join(cols, " OR ")+")")
		}
	}
	return strings.Join(parts, " AND "), nil
}

func keyset(order []orderTerm, cursor []*string, args *[]any) string {
	pos := make([]string, len(order))
	for i, v := range cursor {
		if v == nil {
			pos[i] = "NULL"
		} else {
			*args = append(*args, *v)
			pos[i] = fmt.Sprintf("$%d", len(*args))
		}
	}
	branches := []string{}
	for i, o := range order {
		if cursor[i] == nil {
			continue
		}
		prefix := []string{}
		for j := 0; j < i; j++ {
			prefix = append(prefix, qualified(order[j].Column)+" IS NOT DISTINCT FROM "+pos[j])
		}
		col := qualified(o.Column)
		cmp := ">"
		if o.Desc {
			cmp = "<"
		}
		advance := "(" + col + " " + cmp + " " + pos[i] + " OR " + col + " IS NULL)"
		branches = append(branches, "("+strings.Join(append(prefix, advance), " AND ")+")")
	}
	if len(branches) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(branches, " OR ") + ")"
}

func orderSQL(order []orderTerm) string {
	out := make([]string, len(order))
	for i, o := range order {
		out[i] = qualified(o.Column) + " "
		if o.Desc {
			out[i] += "DESC NULLS LAST"
		} else {
			out[i] += "ASC NULLS LAST"
		}
	}
	return strings.Join(out, ",")
}

func qualified(column string) string { return "r." + pgx.Identifier{column}.Sanitize() }
func orderNames(order []orderTerm) []string {
	out := make([]string, len(order))
	for i, o := range order {
		out[i] = o.Column
		if o.Desc {
			out[i] = "-" + out[i]
		}
	}
	return out
}
func appendWhere(old, add string) string {
	if old == "" {
		return add
	}
	return old + " AND " + add
}
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	return strings.ReplaceAll(s, "_", "\\_")
}
func endpointFingerprint(ref string, ep config.Endpoint, params url.Values) string {
	clean := map[string][]string{}
	for k, v := range params {
		if k != "cursor" && k != "limit" {
			clean[k] = v
		}
	}
	if raw, ok := clean["q"]; ok && len(raw) == 1 && raw[0] == "" {
		delete(clean, "q")
	}
	if raw, ok := clean["sort"]; !ok || len(raw) == 0 || raw[0] == "" {
		terms := append([]string{}, ep.Sort...)
		seen := map[string]bool{}
		for _, term := range terms {
			seen[strings.TrimPrefix(term, "-")] = true
		}
		for _, key := range ep.UniqueKey {
			if !seen[key] {
				terms = append(terms, key)
			}
		}
		clean["sort"] = terms
	}
	b, _ := json.Marshal(struct {
		Ref      string
		Endpoint config.Endpoint
		Params   map[string][]string
	}{ref, ep, clean})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func EncodeCursor(query Query, values []*string) (string, error) {
	if len(values) != len(query.Order) {
		return "", fmt.Errorf("cursor has %d values, want %d", len(values), len(query.Order))
	}
	b, err := json.Marshal(cursorPayload{1, query.Fingerprint, values})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	if len(encoded) > 16*1024 {
		return "", fmt.Errorf("cursor too large")
	}
	return encoded, nil
}
func decodeCursor(raw, fingerprint string, n int) ([]*string, error) {
	if len(raw) > 16*1024 {
		return nil, fmt.Errorf("cursor too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	if len(b) > 16*1024 {
		return nil, fmt.Errorf("cursor too large")
	}
	var p cursorPayload
	if json.Unmarshal(b, &p) != nil || p.Version != 1 || p.Fingerprint != fingerprint || len(p.Values) != n {
		return nil, fmt.Errorf("invalid cursor")
	}
	return p.Values, nil
}
