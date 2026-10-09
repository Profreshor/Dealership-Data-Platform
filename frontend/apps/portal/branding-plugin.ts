import { lstatSync, readFileSync } from "node:fs";
import { dirname, extname, relative, resolve, sep } from "node:path";
import { parseDocument } from "yaml";
import type { Plugin } from "vite";
import { themeCSS } from "./branding-tokens";

const moduleID = "virtual:ddp-branding";
const cssID = "virtual:ddp-branding.css";
const publicPrefix = "frontend/apps/portal/public/";

export function readBranding(registryPath: string) {
  const document = parseDocument(readFileSync(registryPath, "utf8"), { uniqueKeys: true });
  if (document.errors.length) throw new Error("Invalid branding registry YAML");
  const registry: unknown = document.toJS({ maxAliasCount: 50 });
  if (!registry || typeof registry !== "object" || !("ddp" in registry)) throw new Error("Branding requires ddp identity");
  const identity = registry.ddp;
  if (!identity || typeof identity !== "object" || !("display_name" in identity) || typeof identity.display_name !== "string" || !identity.display_name.trim()) throw new Error("Branding requires a display name");
  let accent = "#67c7aa";
  let logo: string | null = null;
  if ("branding" in identity) {
    const branding = identity.branding;
    if (!branding || typeof branding !== "object" || Array.isArray(branding)) throw new Error("ddp.branding must be a mapping");
    if ("accent" in branding) {
      if (typeof branding.accent !== "string" || !/^#[\da-f]{6}$/i.test(branding.accent)) throw new Error("Branding accent must be a six-digit hex color");
      accent = branding.accent;
    }
    if ("logo" in branding && branding.logo !== "") {
      if (typeof branding.logo !== "string" || !branding.logo.startsWith(publicPrefix)) throw new Error("Branding logo must be inside frontend/apps/portal/public");
      const asset = branding.logo.slice(publicPrefix.length);
      if (!/^[\w./-]+$/.test(asset) || asset.split("/").some((part) => part === "." || part === ".." || part === "") || ![".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif"].includes(extname(asset))) throw new Error("Branding logo must be a local SVG, PNG, JPEG, WebP or GIF file");
      const project = dirname(resolve(registryPath));
      const filename = resolve(project, publicPrefix, asset);
      // Check every path component: Vite otherwise follows public-directory links.
      let current = project;
      for (const part of relative(project, filename).split(sep)) {
        current = resolve(current, part);
        if (lstatSync(current).isSymbolicLink()) throw new Error("Branding logo paths cannot contain symlinks");
      }
      const info = lstatSync(filename);
      if (!info.isFile() || info.size === 0 || info.size > 1024 * 1024) throw new Error("Branding logo must be a nonempty file of at most 1 MiB");
      logo = `/${asset}`;
    }
  }
  return { display_name: identity.display_name, logo, accent };
}

export default function brandingPlugin(registryPath: string): Plugin {
  return {
    name: "ddp-branding",
    enforce: "pre",
    buildStart() { readBranding(registryPath); this.addWatchFile(registryPath); },
    resolveId(id) { if (id === moduleID || id === cssID) return `\0${id}`; },
    load(id) {
      if (id !== `\0${moduleID}` && id !== `\0${cssID}`) return null;
      const branding = readBranding(registryPath);
      if (id === `\0${cssID}`) return themeCSS(branding.accent);
      return `import ${JSON.stringify(cssID)}; export default ${JSON.stringify({ display_name: branding.display_name, logo: branding.logo })};`;
    },
  };
}
