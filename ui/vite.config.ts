import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
import tailwind from "@tailwindcss/vite";

const env = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env ?? {};
const PROXY_TARGET = env.LOOPER_PROXY ?? "http://localhost:9090";

export default defineConfig({
  plugins: [solid(), tailwind()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: PROXY_TARGET, changeOrigin: true },
      "/ingest": { target: PROXY_TARGET, changeOrigin: true },
      "/sse": { target: PROXY_TARGET, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    target: "esnext",
  },
});
