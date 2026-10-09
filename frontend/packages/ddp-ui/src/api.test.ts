import { describe, expect, it } from "vitest";
import { z } from "zod";
import { ApiMalformedResponseError, ApiResponseError, createApiClient, portalSchema, sessionSchema, tableDataSchema } from "./api";

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

describe("API data boundary", () => {
  it("validates data and sends the session and CSRF settings", async () => {
    let request: Request | undefined;
    const client = createApiClient({ csrfToken: "csrf", fetcher: async (input, init) => {
      request = new Request(new URL(input.toString(), "http://localhost"), init);
      return response({ ok: true, data: { id: "1" }, error: null });
    } });
    await expect(client.request("/profile", z.object({ id: z.string() }))).resolves.toEqual({ id: "1" });
    expect(request?.credentials).toBe("same-origin");
    expect(request?.headers.get("X-CSRF-Token")).toBe("csrf");
  });

  it("keeps server-provided endpoint URLs absolute", async () => {
    let url = "";
    const client = createApiClient({ fetcher: async (input) => { url = input.toString(); return response({ ok: true, data: null, error: null }); } });
    await client.request("/api/customers", z.null());
    expect(url).toBe("/api/customers");
  });

  it("turns API failures into errors with code and status", async () => {
    const client = createApiClient({ fetcher: () => response({ ok: false, data: null, error: { code: "forbidden", message: "Access denied" } }, 403) });
    await expect(client.request("/admin", z.unknown())).rejects.toMatchObject({ name: "ApiResponseError", code: "forbidden", status: 403 } satisfies Partial<ApiResponseError>);
  });

  const invalidServerData: Array<[z.ZodType<unknown>, unknown]> = [
    [sessionSchema, { user: { id: "1", email: 7, admin: false, permissions: [] }, csrf_token: "x" }],
    [portalSchema, { display_name: "DDP", pages: [{ label: "Customers", path: "/customers", kind: "table" }] }],
    [tableDataSchema, { rows: ["not a row"], next_cursor: null }],
  ];
  it.each(invalidServerData)("rejects server data that violates its schema", async (schema, data) => {
    const client = createApiClient({ fetcher: () => response({ ok: true, data, error: null }) });
    await expect(client.request("/bad", schema)).rejects.toBeInstanceOf(ApiMalformedResponseError);
  });

  it("rejects invalid JSON, malformed envelopes, and contradictory status", async () => {
    const clients = [
      createApiClient({ fetcher: () => Promise.resolve(new Response("not json", { status: 200 })) }),
      createApiClient({ fetcher: () => response({ ok: true, data: null }) }),
      createApiClient({ fetcher: () => response({ ok: true, data: {}, error: null }, 500) }),
    ];
    for (const client of clients) await expect(client.request("/bad", z.unknown())).rejects.toBeInstanceOf(ApiMalformedResponseError);
  });

  it("downloads raw files and turns JSON failures into typed errors", async () => {
    const client = createApiClient({ fetcher: async () => new Response("id\n1\n", { status: 200, headers: { "Content-Type": "text/csv; charset=utf-8" } }) });
    await expect(client.download("/customers/export.csv")).resolves.toBeInstanceOf(Blob);
    const failing = createApiClient({ fetcher: () => response({ ok: false, data: null, error: { code: "revoked", message: "Access denied" } }, 403) });
    await expect(failing.download("/customers/export.csv")).rejects.toMatchObject({ code: "revoked", status: 403 });
    const malformed = createApiClient({ fetcher: async () => new Response("<html>Login</html>", { headers: { "Content-Type": "text/html" } }) });
    await expect(malformed.download("/customers/export.csv")).rejects.toBeInstanceOf(ApiMalformedResponseError);
  });
});
