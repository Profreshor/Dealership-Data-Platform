package serving

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReportingQueriesThroughProtectedHTTP(t *testing.T) {
	pool := portalTestDB(t)
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Serving.PublicURL = "http://localhost:8080"
	cfg.Tables["core.records"] = config.Table{Purpose: "Synthetic reporting records", Contract: config.Contract{
		Columns: map[string]config.Column{
			"id": {Type: "text"}, "name": {Type: "text"}, "city": {Type: "text", Nullable: true},
			"amount": {Type: "numeric", Nullable: true}, "occurred_at": {Type: "timestamptz", Nullable: true},
		}, PrimaryKey: []string{"id"},
	}}
	cfg.Permissions["records.read"] = config.Permission{Description: "Read synthetic records"}
	cfg.Endpoints["records"] = config.Endpoint{Reads: []string{"table/core.records"}, Path: "/api/records", Policy: "permission:records.read",
		Columns: []string{"id", "name", "city", "amount", "occurred_at"}, UniqueKey: []string{"id"}, PageSize: 2,
		Filters: []string{"city", "amount"}, Sort: []string{"city", "-amount"}, Search: []string{"name"}, Export: &config.Export{Format: "csv", MaxRows: 3},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE core.records(id text PRIMARY KEY, name text NOT NULL, city text, amount numeric,
		  occurred_at timestamptz, private_value text DEFAULT 'never-return-this');
		INSERT INTO core.records(id,name,city,amount,occurred_at) VALUES
		 ('a','ALPHA 100%','A',9007199254740993.000001,'2026-09-04T00:00:00.123456Z'),
		 ('b','alpha_other','A',9007199254740993.000002,'2026-09-04T00:00:00.123457Z'),
		 ('c','=SUM(1,2)','B',-2,now()), ('d','normal',NULL,NULL,NULL),
		 ('e','alpha_other','A',2,now()), ('f','_literal','A',2,now());
		GRANT SELECT ON core.records TO ddp_api;
		INSERT INTO app.permissions(name) VALUES ('records.read');
		INSERT INTO app.roles(id,name) VALUES ('report-reader','Report reader');
		INSERT INTO app.role_permissions(role_id,permission) VALUES ('report-reader','records.read');
	`); err != nil {
		t.Fatal(err)
	}
	password, err := auth.HashPassword("synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO app.users(id,email,password_hash) VALUES ('report-user','reader@example.test',$1)", password); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO app.user_roles(user_id,role_id) VALUES ('report-user','report-reader')"); err != nil {
		t.Fatal(err)
	}
	settings := pool.Config()
	settings.ConnConfig.RuntimeParams["role"] = "ddp_api"
	apiPool, err := pgxpool.NewWithConfig(t.Context(), settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(apiPool.Close)
	handler, err := PortalHandler(apiPool, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := portalClient(t)
	paths := []string{"/api/records", "/api/records/row?key.id=a", "/api/records/rows/a", "/api/records/export.csv"}
	for _, path := range paths {
		if got := portalRequest(t, client, "GET", server.URL+path, "", ""); got.status != 401 {
			t.Fatalf("unguarded %s: %+v", path, got)
		}
	}
	login := portalRequest(t, client, "POST", server.URL+"/api/auth/login", `{"email":"reader@example.test","password":"synthetic-password"}`, cfg.Serving.PublicURL)
	if login.status != 200 {
		t.Fatalf("login: %+v", login)
	}
	var cursor string
	var ids []string
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		path := server.URL + "/api/records"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		response := portalRequest(t, client, "GET", path, "", "")
		if response.status != 200 {
			t.Fatalf("page %d: %+v", pageNumber, response)
		}
		var page struct {
			Data httpx.Page `json:"data"`
		}
		if err := json.Unmarshal([]byte(response.body), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Data.Rows) != 2 {
			t.Fatalf("page ceiling: %+v", page)
		}
		for _, row := range page.Data.Rows {
			ids = append(ids, row["id"].(string))
			if _, ok := row["private_value"]; ok {
				t.Fatal("non-declared column leaked")
			}
		}
		if pageNumber == 0 && page.Data.Rows[0]["amount"] != "9007199254740993.000002" {
			t.Fatal("decimal precision lost", page.Data.Rows)
		}
		if page.Data.NextCursor == nil {
			break
		}
		cursor = *page.Data.NextCursor
	}
	if !slices.Equal(ids, []string{"b", "a", "e", "f", "c", "d"}) {
		t.Fatalf("cursor order, ties or null handling: %v", ids)
	}
	for query, expected := range map[string][]string{
		"q=100%25": {"a"}, "q=_literal": {"f"}, "q=AlPhA&limit=10": {"b", "a", "e"},
		"filter.city=B": {"c"}, "filter.amount=9007199254740993.000001": {"a"},
		"filter.city=" + url.QueryEscape("A' OR true --"): {},
	} {
		got := portalRequest(t, client, "GET", server.URL+"/api/records?"+query, "", "")
		if got.status != 200 {
			t.Fatalf("query %s: %+v", query, got)
		}
		var page struct {
			Data httpx.Page `json:"data"`
		}
		if err := json.Unmarshal([]byte(got.body), &page); err != nil {
			t.Fatal(err)
		}
		actual := []string{}
		for _, row := range page.Data.Rows {
			actual = append(actual, row["id"].(string))
		}
		if !slices.Equal(actual, expected) {
			t.Fatalf("query %s: %v != %v", query, actual, expected)
		}
	}
	for _, suffix := range []string{"?limit=1001", "?limit=0", "?limit=1&limit=2", "?sort=private_value", "?filter.name=a", "?q=%zz", "?filter.amount=not-a-number", "?cursor=invalid", "?filter.city=B&cursor=" + url.QueryEscape(cursor), "/export.csv?limit=100", "/row?key.id=a&key.id=b"} {
		got := portalRequest(t, client, "GET", server.URL+"/api/records"+suffix, "", "")
		if got.status != 400 {
			t.Fatalf("invalid request %s: %+v", suffix, got)
		}
	}
	for _, path := range []string{"/api/records/row?key.id=a", "/api/records/rows/a"} {
		got := portalRequest(t, client, "GET", server.URL+path, "", "")
		if got.status != 200 || !strings.Contains(got.body, `"id":"a"`) {
			t.Fatalf("lookup %s: %+v", path, got)
		}
	}
	if got := portalRequest(t, client, "GET", server.URL+"/api/records/rows/missing", "", ""); got.status != 404 {
		t.Fatal("missing lookup", got)
	}
	for _, query := range []string{"", "?filter.city=B"} {
		res, err := client.Get(server.URL + "/api/records/export.csv" + query)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/csv") {
			t.Fatalf("export: %d %s %v", res.StatusCode, body, err)
		}
		records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if query == "" {
			if len(records) != 4 || res.Trailer.Get("X-DDP-Export-Truncated") != "true" || res.Trailer.Get("X-DDP-Export-Rows") != "3" || records[1][3] != "9007199254740993.000002" {
				t.Fatalf("bounded export: %v %v", records, res.Trailer)
			}
		} else if len(records) != 2 || records[1][1] != "'=SUM(1,2)" || res.Trailer.Get("X-DDP-Export-Truncated") != "false" {
			t.Fatalf("filtered safe export: %v %v", records, res.Trailer)
		}
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM app.role_permissions WHERE role_id='report-reader'"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if got := portalRequest(t, client, "GET", server.URL+path, "", ""); got.status != 403 {
			t.Fatalf("revoked route %s: %+v", path, got)
		}
	}
}

func TestReportingSingletonAndInterruptedExport(t *testing.T) {
	pool := portalTestDB(t)
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Serving.PublicURL = "http://localhost:8080"
	cfg.Tables["core.summary"] = config.Table{Purpose: "Synthetic aggregate", Contract: config.Contract{Columns: map[string]config.Column{"total": {Type: "bigint"}}}}
	cfg.Tables["core.broken"] = config.Table{Purpose: "Synthetic export failure", Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer"}, "value": {Type: "text"}}, UniqueKey: []string{"id"}}}
	cfg.Endpoints["summary"] = config.Endpoint{Reads: []string{"table/core.summary"}, Path: "/api/summary", Policy: "public", Columns: []string{"total"}, Shape: "singleton"}
	cfg.Endpoints["broken"] = config.Endpoint{Reads: []string{"table/core.broken"}, Path: "/api/broken", Policy: "public", Columns: []string{"id", "value"}, UniqueKey: []string{"id"}, Export: &config.Export{Format: "csv", MaxRows: 100}}
	if _, err := pool.Exec(t.Context(), `
		CREATE VIEW core.summary AS SELECT 25::bigint AS total;
		CREATE TABLE core.series(id integer PRIMARY KEY);
		INSERT INTO core.series SELECT generate_series(1,25);
		CREATE FUNCTION core.stream_value(i integer) RETURNS text LANGUAGE plpgsql VOLATILE AS $$
		BEGIN
		  IF i=25 THEN RAISE EXCEPTION 'synthetic export failure'; END IF;
		  RETURN repeat('x',10000);
		END $$;
		CREATE VIEW core.broken AS SELECT id, core.stream_value(id) AS value FROM core.series;
	`); err != nil {
		t.Fatal(err)
	}
	handler, err := PortalHandler(pool, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := portalClient(t)
	got := portalRequest(t, client, "GET", server.URL+"/api/summary", "", "")
	if got.status != 200 || !strings.Contains(got.body, `"data":{"total":25}`) {
		t.Fatalf("singleton response: %+v", got)
	}
	if got := portalRequest(t, client, "GET", server.URL+"/api/summary?limit=1", "", ""); got.status != 400 {
		t.Fatalf("singleton accepts pagination: %+v", got)
	}
	if _, err := pool.Exec(t.Context(), "CREATE OR REPLACE VIEW core.summary AS SELECT id::bigint AS total FROM core.series"); err != nil {
		t.Fatal(err)
	}
	if got := portalRequest(t, client, "GET", server.URL+"/api/summary", "", ""); got.status != 500 || !strings.Contains(got.body, "contract_failed") {
		t.Fatalf("singleton contract not enforced: %+v", got)
	}
	if _, err := pool.Exec(t.Context(), "CREATE OR REPLACE VIEW core.summary AS SELECT 1::bigint AS total WHERE false"); err != nil {
		t.Fatal(err)
	}
	if got := portalRequest(t, client, "GET", server.URL+"/api/summary", "", ""); got.status != 404 {
		t.Fatalf("empty singleton: %+v", got)
	}
	blocked, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Rollback(t.Context())
	if _, err := blocked.Exec(t.Context(), "LOCK TABLE core.series IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	if got := portalRequest(t, client, "GET", server.URL+"/api/broken", "", ""); got.status != http.StatusGatewayTimeout || !strings.Contains(got.body, "query_timeout") {
		t.Fatalf("blocked reporting query did not time out: %+v", got)
	}
	if err := blocked.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := client.Get(server.URL + "/api/broken/export.csv")
	if err == nil {
		body, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr == nil || res.Trailer.Get("X-DDP-Export-Rows") != "" || strings.Contains(string(body), "Reporting query failed") {
			t.Fatalf("failed export reported a completed response: status=%d bytes=%d trailers=%v err=%v", res.StatusCode, len(body), res.Trailer, readErr)
		}
	}
}
