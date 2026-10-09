import { lstatSync, readFileSync, readdirSync } from "node:fs";
import { extname, relative, resolve, sep } from "node:path";
import ts from "typescript";
import { parseDocument } from "yaml";
import type { Plugin, ViteDevServer } from "vite";

export type PageRegistration = { id: string; file: string; line: number };

export type PagePluginOptions = { routesRoot: string; registryPath: string };

function sourceFile(file: string, source: string) {
  return ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
}

function importsRegisterPage(file: string, source: string) {
  const ast = sourceFile(file, source);
  return ast.statements.some((statement) => {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier) || statement.moduleSpecifier.text !== "@ddp/ui") return false;
    const bindings = statement.importClause?.namedBindings;
    return Boolean(bindings && ts.isNamedImports(bindings) && bindings.elements.some((item) => item.propertyName?.text === "registerPage" || item.name.text === "registerPage"));
  });
}

function lineOf(file: ts.SourceFile, node: ts.Node) {
  return file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1;
}

function fail(file: string, line: number, message: string): never {
  throw new Error(`Custom page validation failed in ${file}:${line}: ${message}`);
}

/** Parse only direct, top-level registerPage("id", Component) calls. */
export function parsePageRegistrations(file: string, source: string): PageRegistration[] {
  const ast = sourceFile(file, source);
  const aliases = new Set<string>();
  let hasUnsupportedNamespace = false;

  for (const statement of ast.statements) {
    if (ts.isImportDeclaration(statement) && ts.isStringLiteral(statement.moduleSpecifier) && statement.moduleSpecifier.text === "@ddp/ui") {
      const clause = statement.importClause;
      const named = clause?.namedBindings;
      if (named && ts.isNamespaceImport(named)) hasUnsupportedNamespace = true;
      if (named && ts.isNamedImports(named)) {
        for (const item of named.elements) {
          if (item.propertyName?.text === "registerPage" || item.name.text === "registerPage") aliases.add(item.name.text);
        }
      }
    }
    if (ts.isExportDeclaration(statement) && statement.moduleSpecifier && ts.isStringLiteral(statement.moduleSpecifier) && statement.moduleSpecifier.text === "@ddp/ui") {
      if (!statement.isTypeOnly && (!statement.exportClause || ts.isNamedExports(statement.exportClause) && statement.exportClause.elements.some((item) => item.propertyName?.text === "registerPage" || item.name.text === "registerPage"))) {
        fail(file, lineOf(ast, statement), "re-exporting registerPage is unsupported");
      }
    }
  }
  if (hasUnsupportedNamespace) fail(file, 1, "namespace imports from @ddp/ui are unsupported; use a named registerPage import");

  const registrations: PageRegistration[] = [];
  function visit(node: ts.Node) {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return;
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && aliases.has(node.expression.text)) {
      if (node.parent.kind !== ts.SyntaxKind.ExpressionStatement || node.parent.parent !== ast) {
        fail(file, lineOf(ast, node), "registerPage calls must be top-level expression statements");
      }
      const id = node.arguments[0];
      if (!id || !ts.isStringLiteral(id)) fail(file, lineOf(ast, node), "registerPage ID must be a string literal");
      if (node.arguments.length !== 2) fail(file, lineOf(ast, node), "registerPage requires exactly an ID and component");
      if (!ts.isIdentifier(node.arguments[1])) fail(file, lineOf(ast, node), "registerPage component must be an identifier");
      registrations.push({ id: id.text, file, line: lineOf(ast, node) });
    } else if (ts.isIdentifier(node) && aliases.has(node.text) && !(ts.isCallExpression(node.parent) && node.parent.expression === node)) {
      fail(file, lineOf(ast, node), "registerPage must be called directly; indirect references are unsupported");
    }
    ts.forEachChild(node, visit);
  }
  visit(ast);
  return registrations;
}

function routeFiles(root: string): string[] {
  const rootInfo = lstatSync(root, { throwIfNoEntry: false });
  if (!rootInfo) return [];
  if (!rootInfo.isDirectory()) throw new Error(`Route directory must be a directory, not a symlink: ${root}`);
  const files: string[] = [];
  for (const entry of readdirSync(root)) {
    if (entry.startsWith(".") || entry === "node_modules") continue;
    const file = resolve(root, entry);
    const stat = lstatSync(file);
    if (stat.isSymbolicLink()) throw new Error(`Symlinks are unsupported in route modules: ${file}`);
    if (stat?.isDirectory()) files.push(...routeFiles(file));
    else if (stat?.isFile() && [".ts", ".tsx"].includes(extname(file)) && !file.endsWith(".test.ts") && !file.endsWith(".test.tsx")) files.push(file);
  }
  return files;
}

function customPageIds(registryPath: string, source = readFileSync(registryPath, "utf8")): Set<string> {
  const document = parseDocument(source, { uniqueKeys: true });
  if (document.errors.length) throw new Error(`Invalid ${registryPath}: ${document.errors.map((error) => error.message).join("; ")}`);
  const registry: unknown = document.toJS({ maxAliasCount: 50 });
  if (!registry || typeof registry !== "object" || Array.isArray(registry)) throw new Error(`Invalid ${registryPath}: registry must be a mapping`);
  const pages = (registry as Record<string, unknown>).pages;
  if (pages === null || pages === undefined) throw new Error(`Invalid ${registryPath}: pages section is required`);
  if (typeof pages !== "object" || Array.isArray(pages)) throw new Error(`Invalid ${registryPath}: pages must be a mapping`);
  const ids = new Set<string>();
  for (const [id, value] of Object.entries(pages as Record<string, unknown>)) {
    if (value && typeof value === "object" && !Array.isArray(value) && (value as Record<string, unknown>).kind === "custom") ids.add(id);
  }
  return ids;
}

function validate(options: PagePluginOptions, changed?: { file: string; source: string }) {
  const read = (file: string) => file === changed?.file ? changed.source : readFileSync(file, "utf8");
  const expected = customPageIds(options.registryPath, read(options.registryPath));
  const registrations = routeFiles(options.routesRoot).flatMap((file) => {
    const found = parsePageRegistrations(file, read(file));
    if (extname(file) === ".ts" && found.length) fail(file, found[0].line, "registerPage is only allowed in runtime-loaded .tsx route modules");
    return found;
  });
  const seen = new Map<string, PageRegistration>();
  for (const registration of registrations) {
    const prior = seen.get(registration.id);
    if (prior) fail(registration.file, registration.line, `duplicate registration for ${JSON.stringify(registration.id)} (already registered in ${prior.file}:${prior.line})`);
    if (!expected.has(registration.id)) fail(registration.file, registration.line, `unknown or non-custom page ID ${JSON.stringify(registration.id)}`);
    seen.set(registration.id, registration);
  }
  for (const id of expected) if (!seen.has(id)) throw new Error(`Custom page validation failed: page ${JSON.stringify(id)} has no route implementation`);
}

export function createCustomPagesPlugin(options: PagePluginOptions): Plugin {
  const routesRoot = resolve(options.routesRoot);
  const registryPath = resolve(options.registryPath);
  const config = { routesRoot, registryPath };
  const affectsPages = (file: string) => resolve(file) === registryPath || resolve(file).startsWith(`${routesRoot}/`);
  function refresh(server: ViteDevServer, changed?: { file: string; source: string }) {
    server.moduleGraph.invalidateAll();
    try {
      validate(config, changed);
      server.ws.send({ type: "full-reload" });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      server.config.logger.error(message);
      server.ws.send({ type: "error", err: { message, stack: message } });
    }
  }
  const isLoadedRoute = (id: string) => {
    const file = resolve(id.split("?", 1)[0]);
    const parts = relative(routesRoot, file).split(sep);
    return file.startsWith(`${routesRoot}/`) && !parts.some((part) => part.startsWith(".") || part === "node_modules") && extname(file) === ".tsx" && !file.endsWith(".test.tsx");
  };
  return {
    name: "ddp-custom-pages",
    enforce: "pre",
    buildStart() { validate(config); },
    configureServer(server) {
      server.watcher.add([registryPath, routesRoot]);
      const changed = (file: string) => { if (affectsPages(file)) refresh(server); };
      server.watcher.on("add", changed).on("unlink", changed);
      server.httpServer?.once("close", () => {
        server.watcher.off("add", changed).off("unlink", changed);
      });
    },
    transform(code, id) {
      // Recheck after a hard reload too: invalid YAML must not serve a cached entry.
      if (resolve(id.split("?", 1)[0]) === resolve(routesRoot, "../main.tsx")) validate(config);
      if (!id.includes("node_modules") && [".ts", ".tsx"].includes(extname(id.split("?", 1)[0]))) {
        parsePageRegistrations(id, code);
        if (importsRegisterPage(id, code) && !isLoadedRoute(id)) throw new Error(`Custom page validation failed in ${id}: registerPage imports are only allowed in loaded .tsx route modules`);
      }
      return null;
    },
    async handleHotUpdate({ file, server, read }) {
      if (affectsPages(file)) {
        // Vite waits for editors that truncate a file before writing its contents.
        refresh(server, { file: resolve(file), source: await read() });
        return [];
      }
    },
  };
}

export default createCustomPagesPlugin;
