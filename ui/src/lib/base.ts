/**
 * Runtime mount point of the panel. The Go server injects it into index.html
 * as window.__LOOPER_BASE__ ("/" standalone, "/admin/looper/" when a host
 * embeds the panel under a subpath — see internal/web WithBasePath).
 */
declare global {
  interface Window {
    __LOOPER_BASE__?: string;
  }
}

/** Normalized base: "" when served at root, "/admin/looper" under a subpath. */
export const BASE = (window.__LOOPER_BASE__ ?? "/").replace(/\/+$/, "");

/** Prefix an app-absolute path ("/api/…", "/login") with the mount point. */
export function withBase(path: string): string {
  return `${BASE}${path}`;
}
