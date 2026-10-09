import { z } from "zod";

export const apiErrorSchema = z.object({ code: z.string(), message: z.string() });
export type ApiError = z.infer<typeof apiErrorSchema>;

export class ApiResponseError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(error: ApiError, status: number) {
    super(error.message);
    this.name = "ApiResponseError";
    this.code = error.code;
    this.status = status;
  }
}

export class ApiMalformedResponseError extends Error {
  constructor() {
    super("The server returned an invalid response.");
    this.name = "ApiMalformedResponseError";
  }
}

export type ApiClientOptions = { baseUrl?: string; csrfToken?: string; fetcher?: typeof fetch };

export function createApiClient(options: ApiClientOptions = {}) {
  const fetcher = options.fetcher ?? fetch;
  const baseUrl = options.baseUrl ?? "/api";
  return {
    async request<T>(path: string, schema: z.ZodType<T>, init: RequestInit = {}): Promise<T> {
      const headers = new Headers(init.headers);
      headers.set("Accept", "application/json");
      if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
      if (options.csrfToken) headers.set("X-CSRF-Token", options.csrfToken);
      const url = path === "/api" || path.startsWith("/api/") ? path : `${baseUrl}${path}`;
      const response = await fetcher(url, { ...init, headers, credentials: "same-origin" });
      let body: unknown;
      try {
        body = await response.json();
      } catch {
        throw new ApiMalformedResponseError();
      }
      const envelope = z.object({ ok: z.boolean(), data: z.unknown().nullable(), error: apiErrorSchema.nullable() }).safeParse(body);
      if (!envelope.success || response.ok !== envelope.data.ok) throw new ApiMalformedResponseError();
      if (!envelope.data.ok) {
        if (!envelope.data.error || envelope.data.data !== null) throw new ApiMalformedResponseError();
        throw new ApiResponseError(envelope.data.error, response.status);
      }
      if (envelope.data.error !== null) throw new ApiMalformedResponseError();
      const data = schema.safeParse(envelope.data.data);
      if (!data.success) throw new ApiMalformedResponseError();
      return data.data;
    },
    async download(path: string, init: RequestInit = {}): Promise<Blob> {
      const headers = new Headers(init.headers);
      headers.set("Accept", "text/csv");
      if (options.csrfToken) headers.set("X-CSRF-Token", options.csrfToken);
      const url = path === "/api" || path.startsWith("/api/") ? path : `${baseUrl}${path}`;
      const response = await fetcher(url, { ...init, headers, credentials: "same-origin" });
      if (!response.ok) {
        let body: unknown;
        try { body = await response.json(); } catch { throw new ApiMalformedResponseError(); }
        const parsed = z.object({ ok: z.literal(false), data: z.null(), error: apiErrorSchema }).safeParse(body);
        if (!parsed.success) throw new ApiMalformedResponseError();
        throw new ApiResponseError(parsed.data.error, response.status);
      }
      if (response.headers.get("Content-Type")?.split(";")[0]?.trim().toLowerCase() !== "text/csv") throw new ApiMalformedResponseError();
      return response.blob();
    },
  };
}

export const sessionSchema = z.object({
  user: z.object({ id: z.string().min(1), email: z.string().min(1), admin: z.boolean(), permissions: z.array(z.string()) }),
  csrf_token: z.string().min(1),
});
export type Session = z.infer<typeof sessionSchema>;

const pageSchema = z.object({
  id: z.string().min(1),
  label: z.string().min(1),
  path: z.string().regex(/^\/(?!\/)/),
});
export const portalSchema = z.object({
  display_name: z.string().min(1),
  pages: z.array(z.discriminatedUnion("kind", [
    pageSchema.extend({
      kind: z.literal("table"),
      endpoint: z.string().regex(/^\/api\/(?!\/)/),
      columns: z.array(z.string().min(1)).min(1),
      filters: z.array(z.string().min(1)),
      sort: z.array(z.string().min(1)),
      search: z.array(z.string().min(1)),
      page_size: z.number().int().min(1).max(1000),
      shape: z.enum(["list", "singleton"]),
      export: z.object({ format: z.literal("csv"), max_rows: z.number().int().positive() }).optional(),
    }),
    pageSchema.extend({ kind: z.enum(["custom", "system"]) }),
  ])),
});
export type Portal = z.infer<typeof portalSchema>;
export type PortalPage = Portal["pages"][number];

export const tableDataSchema = z.object({ rows: z.array(z.record(z.unknown())), next_cursor: z.string().nullable() });
export type TableData = z.infer<typeof tableDataSchema>;
export const singletonDataSchema = z.record(z.unknown());
