import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { createCustomPagesPlugin, parsePageRegistrations } from "./custom-pages-plugin";

const parse = (code: string) => parsePageRegistrations("src/routes/example.tsx", code);

describe("custom page registration validation", () => {
  it("ignores comments and strings, and accepts named aliases and helper imports", () => {
    const registrations = parse(`
      import { registerPage as register } from "@ddp/ui";
      import { registerPage as helper } from "other";
      const text = 'register("fake", Fake)';
      // register("comment", Fake)
      register("dashboard", Dashboard);
      helper("helper", Helper);
    `);
    expect(registrations.map(({ id }) => id)).toEqual(["dashboard"]);
  });

  it("accepts multiple direct registrations", () => {
    expect(parse(`import { registerPage } from "@ddp/ui"; registerPage("a", A); registerPage("b", B);`)).toHaveLength(2);
  });

  it.each([
    ["missing component", `import { registerPage } from "@ddp/ui"; registerPage("a");`],
    ["dynamic id", `import { registerPage } from "@ddp/ui"; const id = "a"; registerPage(id, A);`],
    ["dynamic component", `import { registerPage } from "@ddp/ui"; registerPage("a", components.A);`],
    ["nested call", `import { registerPage } from "@ddp/ui"; function add() { registerPage("a", A); }`],
    ["indirect call", `import { registerPage } from "@ddp/ui"; const add = registerPage; add("a", A);`],
  ])("rejects %s", (_, code) => expect(() => parse(code)).toThrow());

  it("rejects namespace imports and duplicate IDs during a real plugin validation", () => {
    const root = mkdtempSync(resolve(tmpdir(), "ddp-pages-"));
    const routes = resolve(root, "src/routes");
    mkdirSync(routes, { recursive: true });
    writeFileSync(resolve(root, "ddp.yaml"), "pages:\n  dashboard: { kind: custom }\n");
    writeFileSync(resolve(routes, "dashboard.tsx"), `import { registerPage } from "@ddp/ui"; registerPage("dashboard", A); registerPage("dashboard", B);`);
    const plugin = createCustomPagesPlugin({ routesRoot: routes, registryPath: resolve(root, "ddp.yaml") });
    expect(() => plugin.buildStart?.call({} as never)).toThrow(/duplicate registration/);
    writeFileSync(resolve(routes, "dashboard.tsx"), `import * as ui from "@ddp/ui"; ui.registerPage("dashboard", A);`);
    expect(() => plugin.buildStart?.call({} as never)).toThrow(/namespace imports/);
    rmSync(root, { recursive: true, force: true });
  });

  it("rejects missing and unknown or non-custom pages and malformed YAML", () => {
    const root = mkdtempSync(resolve(tmpdir(), "ddp-pages-"));
    const routes = resolve(root, "src/routes");
    mkdirSync(routes, { recursive: true });
    const check = (yaml: string, code: string, message?: RegExp) => {
      writeFileSync(resolve(root, "ddp.yaml"), yaml);
      writeFileSync(resolve(routes, "page.tsx"), code);
      const plugin = createCustomPagesPlugin({ routesRoot: routes, registryPath: resolve(root, "ddp.yaml") });
      expect(() => plugin.buildStart?.call({} as never)).toThrow(message);
    };
    check("pages:\n  dashboard: { kind: custom }\n", `import { registerPage } from "@ddp/ui";`);
    check("pages:\n  dashboard: { kind: table }\n", `import { registerPage } from "@ddp/ui"; registerPage("dashboard", A);`);
    check("pages:\n  dashboard: { kind: custom }\n", `import { registerPage } from "@ddp/ui"; registerPage("missing", A);`);
    check("pages: [", "");
    for (const yaml of ["", "null", "false", "[]", "scalar"]) check(yaml, "", /registry must be a mapping/);
    rmSync(root, { recursive: true, force: true });
  });
});
