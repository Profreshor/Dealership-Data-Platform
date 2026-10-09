import { useMutation, useQuery } from "@tanstack/react-query";
import { Fragment, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { ApiResponseError, createApiClient, singletonDataSchema, tableDataSchema, type PortalPage, type TableData } from "./api";
import { DataTable } from "./table";

type TablePage = Extract<PortalPage, { kind: "table" }>;
type Criteria = { search: string; filters: Record<string, { enabled: boolean; value: string }>; sort: string };

function queryString(page: TablePage, criteria: Criteria, cursor?: string, includePaging = true) {
  const query = new URLSearchParams();
  if (criteria.search && page.search.length) query.set("q", criteria.search);
  for (const name of page.filters) {
    const filter = criteria.filters[name];
    if (filter?.enabled) query.set(`filter.${name}`, filter.value);
  }
  if (criteria.sort) query.set("sort", criteria.sort);
  if (includePaging && page.shape === "list") {
    query.set("limit", String(page.page_size));
    if (cursor) query.set("cursor", cursor);
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function sortColumn(term: string) { return term.replace(/^[+-]/, ""); }

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

export function EndpointTable({ page }: { page: TablePage }) {
  const defaults = useMemo<Criteria>(() => ({ search: "", filters: Object.fromEntries(page.filters.map((name) => [name, { enabled: false, value: "" }])), sort: page.sort.join(",") }), [page.filters, page.sort]);
  const [draft, setDraft] = useState(defaults);
  const [applied, setApplied] = useState(defaults);
  const [cursors, setCursors] = useState<string[]>([]);
  const query = queryString(page, applied, cursors.at(-1));
  const tableQuery = useQuery({
    queryKey: ["table", page.endpoint, page.shape, query],
    queryFn: async ({ signal }): Promise<TableData> => {
      const client = createApiClient();
      if (page.shape === "list") return client.request(page.endpoint + query, tableDataSchema, { signal });
      try {
        const row = await client.request(page.endpoint + query, singletonDataSchema, { signal });
        return { rows: [row], next_cursor: null };
      } catch (error) {
        if (error instanceof ApiResponseError && error.status === 404) return { rows: [], next_cursor: null };
        throw error;
      }
    },
  });
  const exportMutation = useMutation({
    mutationFn: ({ signal }: { signal?: AbortSignal }) => createApiClient().download(`${page.endpoint}/export.csv${queryString(page, applied, undefined, false)}`, { signal }),
    onSuccess: (blob) => downloadBlob(blob, `${page.label.toLowerCase().replace(/[^a-z0-9]+/g, "-") || "export"}.csv`),
  });
  const rows = tableQuery.data?.rows ?? [];
  const hasCriteria = Boolean(applied.search || Object.values(applied.filters).some((filter) => filter.enabled) || applied.sort !== page.sort.join(","));

  function apply(event: FormEvent) {
    event.preventDefault();
    setCursors([]);
    setApplied({ search: draft.search, filters: { ...draft.filters }, sort: draft.sort });
  }
  function reset() {
    setDraft(defaults);
    setCursors([]);
    setApplied(defaults);
  }

  return <section aria-labelledby="page-title">
    <div className="page-heading"><div><h1 id="page-title">{page.label}</h1><p>Live records from your workspace.</p></div>{tableQuery.data && !tableQuery.error && <span className="row-count">{rows.length} {rows.length === 1 ? "row" : "rows"}</span>}</div>
    {(page.search.length > 0 || page.filters.length > 0 || page.sort.length > 0) && <form className="table-controls" onSubmit={apply}>
      {page.search.length > 0 && <div className="field"><label htmlFor="table-search">Search</label><input id="table-search" value={draft.search} onChange={(event) => setDraft({ ...draft, search: event.target.value })} /></div>}
      {page.filters.map((name) => { const filter = draft.filters[name]; return <div className="table-filter" key={name}><label><input type="checkbox" checked={filter?.enabled ?? false} onChange={(event) => setDraft({ ...draft, filters: { ...draft.filters, [name]: { ...(filter ?? { value: "" }), enabled: event.target.checked } } })} /> Filter {name.replaceAll("_", " ")}</label><input aria-label={`${name} exact value`} value={filter?.value ?? ""} onChange={(event) => setDraft({ ...draft, filters: { ...draft.filters, [name]: { enabled: filter?.enabled ?? false, value: event.target.value } } })} /></div>; })}
      {page.sort.length > 0 && <div className="field"><label htmlFor="table-sort">Sort</label><select id="table-sort" value={draft.sort} onChange={(event) => setDraft({ ...draft, sort: event.target.value })}><option value={page.sort.join(",")}>Default</option>{page.sort.map((term) => { const name = sortColumn(term); const rest = page.sort.filter((item) => sortColumn(item) !== name); return <Fragment key={name}><option value={[name, ...rest].join(",")}>{name.replaceAll("_", " ")} ascending</option><option value={[`-${name}`, ...rest].join(",")}>{name.replaceAll("_", " ")} descending</option></Fragment>; })}</select></div>}
      <div className="table-actions"><button className="primary-button" type="submit">Apply</button><button type="button" onClick={reset}>Reset</button></div>
    </form>}
    {tableQuery.isPending && <div className="loading table-loading" role="status"><span aria-hidden="true" /><span>Loading {page.label.toLowerCase()}…</span></div>}
    {tableQuery.error && <div className="page-error" role="alert"><strong>Couldn’t load this table.</strong><span>{tableQuery.error instanceof Error ? tableQuery.error.message : "Something went wrong. Try again."}</span><button type="button" onClick={() => tableQuery.refetch()}>Try again</button></div>}
    {!tableQuery.isPending && !tableQuery.error && !rows.length && <div className="empty-state"><strong>{hasCriteria ? "No matching rows" : "No rows yet"}</strong><span>{hasCriteria ? "Try changing the filters or search." : "This table will update when its source data arrives."}</span></div>}
    {!tableQuery.isPending && !tableQuery.error && rows.length > 0 && <DataTable label={page.label} columns={page.columns} rows={rows} />}
    {page.shape === "list" && !tableQuery.error && <nav className="table-pagination" aria-label="Table pages"><button type="button" disabled={!cursors.length || tableQuery.isFetching} onClick={() => setCursors((items) => items.slice(0, -1))}>Previous</button><span>Page {cursors.length + 1}</span><button type="button" disabled={!tableQuery.data || !tableQuery.data.next_cursor || tableQuery.isFetching} onClick={() => { const cursor = tableQuery.data?.next_cursor; if (cursor) setCursors((items) => [...items, cursor]); }}>Next</button></nav>}
    {page.export && <div className="table-export"><span>Export includes the current filters and sort, up to {page.export.max_rows.toLocaleString()} rows.</span><button type="button" disabled={exportMutation.isPending} onClick={() => exportMutation.mutate({})}>{exportMutation.isPending ? "Preparing download…" : "Download CSV"}</button>{exportMutation.error && <span role="alert">Download failed: {exportMutation.error instanceof Error ? exportMutation.error.message : "Try again."}</span>}</div>}
  </section>;
}
