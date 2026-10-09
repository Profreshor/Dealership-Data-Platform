import { useQuery } from "@tanstack/react-query";
import { createApiClient, registerPage, tableDataSchema } from "@ddp/ui";

function Report() {
  const observation = useQuery({ queryKey: ["custom-proof"], queryFn: () => createApiClient().request("/custom-proof", tableDataSchema) });
  return <section aria-labelledby="page-title">
    <div className="page-heading"><div><h1 id="page-title">Synthetic observation</h1><p>A client page using the shared API client.</p></div></div>
    {observation.isPending && <p role="status">Loading observation…</p>}
    {observation.error && <p role="alert">Observation unavailable.</p>}
    {observation.data && <p role="status">Observation {String(observation.data.rows[0]?.count)}</p>}
    <button className="primary-button" type="button" onClick={() => observation.refetch()}>Refresh observation</button>
  </section>;
}

registerPage("custom_report", Report);
