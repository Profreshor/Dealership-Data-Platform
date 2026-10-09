import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import customPages from "./custom-pages-plugin";
import brandingPlugin from "./branding-plugin";

export default defineConfig({
  plugins: [brandingPlugin(fileURLToPath(new URL("../../../ddp.yaml", import.meta.url))), customPages({ routesRoot: fileURLToPath(new URL("./src/routes", import.meta.url)), registryPath: fileURLToPath(new URL("../../../ddp.yaml", import.meta.url)) }), react(), tailwindcss()],
  build: { outDir: "../../dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://localhost:8080" } },
});
