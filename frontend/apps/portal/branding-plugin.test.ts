import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import brandingPlugin, { readBranding } from "./branding-plugin";
import customPages from "./custom-pages-plugin";
import { createServer } from "vite";

const scratch: string[] = [];
afterEach(async () => { await Promise.all(scratch.splice(0).map((path) => rm(path, { recursive: true, force: true }))); });
async function fixture() {
  const root = await mkdtemp(resolve(tmpdir(), "ddp-branding-")); scratch.push(root);
  const registry = resolve(root, "ddp.yaml");
  const publicRoot = resolve(root, "frontend/apps/portal/public");
  await mkdir(publicRoot, { recursive: true });
  const set = (branding: unknown) => writeFile(registry, JSON.stringify({ ddp: { display_name: "Synthetic & Co", branding }, pages: {} }));
  return { root, registry, publicRoot, set };
}
describe("branding assets", () => {
  it("keeps default branding and resolves a packaged local logo", async () => {
    const f = await fixture();
    await writeFile(f.registry, 'ddp: { display_name: "Synthetic & Co" }');
    expect(readBranding(f.registry)).toEqual({ display_name: "Synthetic & Co", logo: null, accent: "#67c7aa" });
    await writeFile(resolve(f.publicRoot, "logo.svg"), '<svg xmlns="http://www.w3.org/2000/svg"/>');
    await f.set({ logo: "frontend/apps/portal/public/logo.svg", accent: "#2457c5" });
    expect(readBranding(f.registry)).toEqual({ display_name: "Synthetic & Co", logo: "/logo.svg", accent: "#2457c5" });
  });
  it("refuses unsafe, missing, empty and oversized assets and malformed colors", async () => {
    const f = await fixture();
    for (const logo of ["https://example.test/a.svg", "frontend/apps/portal/public/../private.svg", "frontend/apps/portal/public/sub/../logo.svg", "frontend/apps/portal/public/a.svg?token=secret", "frontend/apps/portal/public/a.html", "frontend/apps/portal/public/missing.svg"]) {
      await f.set({ logo }); expect(() => readBranding(f.registry), logo).toThrow();
    }
    await writeFile(resolve(f.publicRoot,"empty.svg"), "");
    await f.set({ logo:"frontend/apps/portal/public/empty.svg" }); expect(() => readBranding(f.registry)).toThrow(/nonempty/);
    await writeFile(resolve(f.publicRoot,"huge.png"), new Uint8Array(1024*1024+1));
    await f.set({ logo:"frontend/apps/portal/public/huge.png" }); expect(() => readBranding(f.registry)).toThrow(/1 MiB/);
    for (const accent of ["red", "#fff", "#ffffff;}", 1, null]) { await f.set({ accent }); expect(() => readBranding(f.registry)).toThrow(/hex color/); }
  });
  it("refuses a logo symlink or linked parent even when the target is in the project", async () => {
    const f=await fixture();
    await writeFile(resolve(f.root,"private.svg"),"private");
    await symlink(resolve(f.root,"private.svg"),resolve(f.publicRoot,"logo.svg"));
    await f.set({logo:"frontend/apps/portal/public/logo.svg"}); expect(()=>readBranding(f.registry)).toThrow(/symlink/);
    await symlink(f.root,resolve(f.publicRoot,"linked"));
    await f.set({logo:"frontend/apps/portal/public/linked/private.svg"}); expect(()=>readBranding(f.registry)).toThrow(/symlink/);
  });
  it("reloads branding modules after registry changes in a real Vite server", async () => {
    const f = await fixture();
    const app = dirname(f.publicRoot), routes = resolve(app,"src/routes");
    await mkdir(routes,{recursive:true});
    await writeFile(resolve(app,"src/main.tsx"),'import branding from "virtual:ddp-branding"; export default branding;');
    await f.set({accent:"#2457c5"});
    const server = await createServer({root:app,configFile:false,plugins:[brandingPlugin(f.registry),customPages({registryPath:f.registry,routesRoot:routes})],server:{host:"127.0.0.1",port:0}});
    const events: unknown[]=[];
    const send=server.ws.send.bind(server.ws);
    server.ws.send=(payload)=>{events.push(payload);return send(payload);};
    try {
      await server.listen();
      const origin=server.resolvedUrls!.local[0];
      const read=async (id:string)=>(await fetch(new URL(`/@id/__x00__virtual:ddp-branding${id}`,origin))).text();
      expect(await read("")).toContain("Synthetic & Co");
      const originalCSS=await read(".css");
      expect(originalCSS).toContain("--accent:");
      await f.set({accent:"#ff0000"});
      await expect.poll(()=>events.some((event)=>JSON.stringify(event).includes("full-reload"))).toBe(true);
      expect(await read(".css")).not.toBe(originalCSS);
      // Clear Vite/Chokidar's 50 ms duplicate-change window before the next edit.
      await new Promise((done)=>setTimeout(done,125));
      events.length=0;
      await f.set({accent:"not-a-color"});
      await expect.poll(()=>events.some((event)=>JSON.stringify(event).includes("full-reload"))).toBe(true);
      const invalid=await fetch(new URL("/@id/__x00__virtual:ddp-branding",origin));
      expect(invalid.status).toBe(500);
      expect(await invalid.text()).toContain("six-digit");
    } finally {await server.close();}
  });

});
