package endpoints

import (
	"encoding/base64"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testEndpoint() config.Endpoint {
	return config.Endpoint{
		Reads: []string{"model/mart.orders"}, Columns: []string{"id", "amount", "created_at", "name"},
		Filters: []string{"name"}, Sort: []string{"amount", "-created_at"}, Search: []string{"name"},
		UniqueKey: []string{"id"}, Export: &config.Export{Format: "csv", MaxRows: 100},
	}
}

func TestPlanKeysetUsesTypedColumnsAndEscapedSearch(t *testing.T) {
	ep := testEndpoint()
	q, err := Plan("endpoint/orders", ep, url.Values{"q": {"50%_\\"}, "sort": {"-amount"}}, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.SQL, `"amount" < $`) || strings.Contains(q.SQL, `"amount" IS NULL`) {
		t.Fatalf("unexpected keyset comparison without cursor: %s", q.SQL)
	}
	if strings.Contains(q.SQL, `"amount"::text`) == false {
		t.Fatalf("missing hidden cursor value: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, `ESCAPE E'\\'`) {
		t.Fatalf("missing LIKE escape: %s", q.SQL)
	}
	if got := q.Args[0]; got != `50\%\_\\` {
		t.Fatalf("escaped search = %q", got)
	}
}

func TestCursorBindsFingerprintAndNullableValues(t *testing.T) {
	ep := testEndpoint()
	q, err := Plan("endpoint/orders", ep, url.Values{"sort": {"amount,-created_at"}}, "list")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := EncodeCursor(q, []*string{nil, strptr("2026-01-01T00:00:00.123456Z"), strptr("id")})
	if err != nil {
		t.Fatal(err)
	}
	q2, err := Plan("endpoint/orders", ep, url.Values{"sort": {"amount,-created_at"}, "cursor": {cursor}}, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q2.SQL, `r."amount" IS NOT DISTINCT FROM NULL`) || !strings.Contains(q2.SQL, `r."created_at" < $`) {
		t.Fatalf("unexpected nullable keyset: %s", q2.SQL)
	}
	bad := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"f":"tampered","values":[null,"x","id"]}`))
	if _, err := Plan("endpoint/orders", ep, url.Values{"cursor": {bad}}, "list"); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}

func TestPlanRejectsDuplicatesAndInjection(t *testing.T) {
	ep := testEndpoint()
	if _, err := Plan("endpoint/orders", ep, url.Values{"q": {"a", "b"}}, "list"); err == nil {
		t.Fatal("duplicate parameter accepted")
	}
	if _, err := Plan("endpoint/orders", ep, url.Values{"sort": {"amount;drop table x"}}, "list"); err == nil {
		t.Fatal("sort injection accepted")
	}
	if _, err := Plan("endpoint/orders", ep, url.Values{"filter.name": {"x' OR 1=1 --"}}, "list"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanExecutesAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	p, err := pgxpool.New(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	conn, err := p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), `CREATE TEMP TABLE ddp_query_acceptance (tenant text, id text, amount numeric, created_at timestamptz, name text)`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(t.Context(), `INSERT INTO ddp_query_acceptance VALUES ('t','a','9007199254740993.000001','2026-01-01 00:00:00.123456+00',E'50%_\\'),('t','b',NULL,'2026-01-01 00:00:00.123455+00','other'),('t','c','9007199254740993.000000','2026-01-01 00:00:00.123457+00',E'50%_\\')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO ddp_query_acceptance VALUES
	 ('t','e','9007199254740993.000001','2026-01-01 00:00:00.123457+00','tie'),
	 ('t','f','9007199254740993.000001','2026-01-01 00:00:00.123457+00','tie'),
	 ('t','g','9007199254740993.000001',NULL,'null time')`); err != nil {
		t.Fatal(err)
	}
	ep := testEndpoint()
	ep.Reads = []string{"table/pg_temp.ddp_query_acceptance"}
	ep.Columns = []string{"id", "amount", "created_at", "name"}
	ep.UniqueKey = []string{"id"}
	ep.Export = &config.Export{Format: "csv", MaxRows: 1}
	params := url.Values{"sort": {"amount,-created_at"}, "limit": {"1"}}
	var ids []string
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		q, e := Plan("endpoint/acceptance", ep, params, "list")
		if e != nil {
			t.Fatal(e)
		}
		rows, e := conn.Query(t.Context(), q.SQL, q.Args...)
		if e != nil {
			t.Fatal(e)
		}
		var page [][]any
		for rows.Next() {
			v, e := rows.Values()
			if e != nil {
				rows.Close()
				t.Fatal(e)
			}
			page = append(page, v)
		}
		if e := rows.Err(); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		rows.Close()
		if len(page) > 2 {
			t.Fatalf("fetch limit+1 returned %d rows", len(page))
		}
		if len(page) == 0 {
			break
		}
		id, ok := page[0][0].(string)
		if !ok {
			t.Fatalf("id type = %T", page[0][0])
		}
		ids = append(ids, id)
		vals := make([]*string, len(q.Order))
		for i := range vals {
			if page[0][len(ep.Columns)+i] != nil {
				s, ok := page[0][len(ep.Columns)+i].(string)
				if !ok {
					t.Fatalf("cursor type = %T", page[0][len(ep.Columns)+i])
				}
				vals[i] = &s
			}
		}
		cursor, e := EncodeCursor(q, vals)
		if e != nil {
			t.Fatal(e)
		}
		params = url.Values{"sort": {"amount,-created_at"}, "limit": {"1"}, "cursor": {cursor}}
	}
	if strings.Join(ids, ",") != "c,e,f,a,g,b" {
		t.Fatalf("keyset order = %v", ids)
	}
	search, err := Plan("endpoint/acceptance", ep, url.Values{"q": {`50%_\`}}, "list")
	if err != nil {
		t.Fatal(err)
	}
	searchRows, err := conn.Query(t.Context(), search.SQL, search.Args...)
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for searchRows.Next() {
		values, err := searchRows.Values()
		if err != nil {
			t.Fatal(err)
		}
		matches = append(matches, values[0].(string))
	}
	if err := searchRows.Err(); err != nil {
		t.Fatal(err)
	}
	searchRows.Close()
	if strings.Join(matches, ",") != "c,a" {
		t.Fatalf("literal search escaped incorrectly: %v", matches)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO ddp_query_acceptance(tenant,id) VALUES ('other','a')`); err != nil {
		t.Fatal(err)
	}
	getEp := ep
	getEp.Columns = []string{"tenant", "id"}
	getEp.UniqueKey = []string{"tenant", "id"}
	gq, err := Plan("endpoint/acceptance", getEp, url.Values{"key.tenant": {"t"}, "key.id": {"a"}}, "get")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(t.Context(), gq.SQL, gq.Args...)
	if err != nil {
		t.Fatal(err)
	}
	var found [][]any
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(found) != 1 || found[0][0] != "t" || found[0][1] != "a" {
		t.Fatalf("composite lookup failed: %v", found)
	}
	eq, err := Plan("endpoint/acceptance", ep, url.Values{"sort": {"amount"}}, "export")
	if err != nil {
		t.Fatal(err)
	}
	rows, err = conn.Query(t.Context(), eq.SQL, eq.Args...)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count != 2 {
		t.Fatalf("export fetch limit+1 = %d", count)
	}
}

func strptr(s string) *string { return &s }
