import { mkdir, mkdtemp, open, readFile, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { createServer, type ViteDevServer } from "vite";
import { afterEach, describe, expect, it } from "vitest";
import { createCustomPagesPlugin, parsePageRegistrations } from "./custom-pages-plugin";

const servers: ViteDevServer[] = [];
const scratch: string[] = [];

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => server.close()));
  await Promise.all(scratch.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});

async function fixture() {
  const root = await mkdtemp(resolve(tmpdir(), "ddp-custom-pages-dev-"));
  scratch.push(root);
  const app = resolve(root, "frontend/apps/portal");
  const routes = resolve(app, "src/routes");
  const registry = resolve(root, "ddp.yaml");
  await mkdir(resolve(app, "node_modules/@ddp/ui"), { recursive: true });
  await mkdir(routes, { recursive: true });
  await writeFile(resolve(app, "node_modules/@ddp/ui/package.json"), '{"name":"@ddp/ui","type":"module","main":"index.ts"}');
  await writeFile(resolve(app, "node_modules/@ddp/ui/index.ts"), "export function registerPage(_id: string, _component: unknown) {}\n");
  await writeFile(resolve(app, "src/main.tsx"), 'import.meta.glob(["./routes/**/*.tsx", "!./routes/**/*.test.tsx"], { eager: true });\nexport default 1;\n');
  await writeFile(resolve(routes, "dashboard.tsx"), 'import { registerPage } from "@ddp/ui";\nfunction Dashboard() { return <div>Dashboard</div>; }\nregisterPage("dashboard", Dashboard);\n');
  await writeFile(registry, "pages:\n  dashboard: { label: Dashboard, path: /, kind: custom }\n");
  return { root: await realpath(root), app: await realpath(app), routes: await realpath(routes), registry: await realpath(registry) };
}

async function start(f: Awaited<ReturnType<typeof fixture>>) {
  const server = await createServer({
    root: f.app,
    configFile: false,
    plugins: [createCustomPagesPlugin({ routesRoot: f.routes, registryPath: f.registry })],
    server: { hmr: true, host: "127.0.0.1", port: 0 },
  });
  servers.push(server);
  await server.listen();
  return server;
}

async function fetchMain(server: ViteDevServer) {
  const url = server.resolvedUrls?.local[0];
  if (!url) throw new Error("Vite did not resolve a local URL");
  return fetch(new URL(`src/main.tsx?reload=${Date.now()}`, url));
}

async function eventually(check: () => boolean, label: string) {
  const deadline = Date.now() + 4_000;
  while (!check() && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 25));
  expect(check(), label).toBe(true);
}

async function eventuallyStatus(server: ViteDevServer, status: number) {
  const deadline = Date.now() + 4_000;
  let actual = 0;
  while (Date.now() < deadline) {
    actual = (await fetchMain(server)).status;
    if (actual === status) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  expect(actual).toBe(status);
}

async function settleWatcher() {
  // Vite 6 bundles Chokidar throttles of 50 ms for change and 100 ms for remove.
  // Distinct test edits must clear those windows even after an HMR message arrives.
  await new Promise((resolve) => setTimeout(resolve, 125));
}

describe("custom page production and dev verification", () => {
  it("revalidates the completed registry after a truncate-before-write save", async () => {
    const f = await fixture();
    const server = await start(f);
    await eventuallyStatus(server, 200);
    await settleWatcher();
    const events: unknown[] = [];
    const send = server.ws.send.bind(server.ws);
    server.ws.send = (payload) => { events.push(payload); return send(payload); };
    const changed = new Promise<void>((resolve) => server.watcher.once("change", () => resolve()));
    const file = await open(f.registry, "w");
    try {
      await changed;
      await new Promise((resolve) => setTimeout(resolve, 10));
      await file.writeFile("pages:\n  dashboard: { label: Dashboard, path: /, kind: custom }\n  reports: { kind: custom }\n");
    } finally {
      await file.close();
    }
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"error"') && JSON.stringify(event).includes("reports")), "completed save reports the missing route");
    expect(events.filter((event) => JSON.stringify(event).includes('"type":"error"')).every((event) => JSON.stringify(event).includes("reports"))).toBe(true);
  });

  it("revalidates an external YAML registry across invalidation, route add/unlink, and hard reload", async () => {
    const f = await fixture();
    const server = await start(f);
    const events: unknown[] = [];
    const send = server.ws.send.bind(server.ws);
    server.ws.send = (payload) => { events.push(payload); return send(payload); };

    await eventuallyStatus(server, 200);
    events.length = 0;
    await writeFile(f.registry, "pages:\n  dashboard: [\n");
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"error"')), "invalid YAML emits a Vite error overlay");
    await settleWatcher();
    const invalid = await fetchMain(server);
    expect(await invalid.text()).toContain("Flow sequence");

    events.length = 0;
    await writeFile(f.registry, "pages:\n  dashboard: { label: Dashboard, path: /, kind: custom }\n  reports: { label: Reports, path: /reports, kind: custom }\n");
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"error"') && JSON.stringify(event).includes("reports")), "missing route is rejected");
    await settleWatcher();
    events.length = 0;
    await writeFile(resolve(f.routes, "reports.tsx"), 'import { registerPage } from "@ddp/ui";\nfunction Reports() { return <div>Reports</div>; }\nregisterPage("reports", Reports);\n');
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"full-reload"')), "route add recovers with a full reload");
    await settleWatcher();
    await eventuallyStatus(server, 200);

    events.length = 0;
    await rm(resolve(f.routes, "reports.tsx"));
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"error"') && JSON.stringify(event).includes("reports")), "route unlink rejects the missing implementation");
    await settleWatcher();
    events.length = 0;
    await writeFile(resolve(f.routes, "reports.tsx"), 'import { registerPage } from "@ddp/ui";\nfunction Reports() { return <div>Reports</div>; }\nregisterPage("reports", Reports);\n');
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"full-reload"')), "route relink recovers");
    await settleWatcher();
    await eventuallyStatus(server, 200);

    events.length = 0;
    await writeFile(f.registry, "pages:\n  dashboard: { label: Renamed dashboard, path: /workspace, kind: custom }\n  reports: { label: Reports, path: /reports, kind: custom }\n");
    await eventually(() => events.some((event) => JSON.stringify(event).includes('"type":"full-reload"')), "registry rename is observed outside Vite root");
    expect(await readFile(f.registry, "utf8")).toContain("dashboard");
    await eventuallyStatus(server, 200);
  }, 20_000);

  it("keeps parser and route walk strict at the trust boundary", async () => {
    expect(parsePageRegistrations("x.tsx", `import { registerPage as r } from "@ddp/ui"; const s = "r('fake', X)"; // r("comment", X)\nr("dashboard", Dashboard);`)).toEqual([{ id: "dashboard", file: "x.tsx", line: 2 }]);
    expect(() => parsePageRegistrations("x.tsx", 'import * as ui from "@ddp/ui"; ui.registerPage("dashboard", Dashboard);')).toThrow(/namespace imports/);
    expect(() => parsePageRegistrations("x.tsx", 'import { registerPage } from "@ddp/ui"; const r = registerPage; r("dashboard", Dashboard);')).toThrow(/indirect/);

    const f = await fixture();
    await writeFile(resolve(f.routes, ".ignored.tsx"), 'import { registerPage } from "@ddp/ui"; registerPage("unknown", Unknown);');
    await writeFile(resolve(f.routes, "ignored.test.tsx"), 'import { registerPage } from "@ddp/ui"; registerPage("unknown", Unknown);');
    await mkdir(resolve(f.routes, "node_modules"));
    await writeFile(resolve(f.routes, "node_modules/ignored.tsx"), 'import { registerPage } from "@ddp/ui"; registerPage("unknown", Unknown);');
    const ignoredPlugin = createCustomPagesPlugin({ routesRoot: f.routes, registryPath: f.registry });
    expect(() => ignoredPlugin.buildStart?.call({} as never)).not.toThrow();
    const linked = resolve(f.routes, "linked");
    await mkdir(resolve(f.root, "elsewhere"));
    await symlink(resolve(f.root, "elsewhere"), linked);
    const plugin = createCustomPagesPlugin({ routesRoot: f.routes, registryPath: f.registry });
    expect(() => plugin.buildStart?.call({} as never)).toThrow(/Symlinks are unsupported/);
  });
});
