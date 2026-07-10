import type { ApiClient } from "./types";
import { httpClient } from "./client";
import { mockClient } from "./mock/client";

const useMock = import.meta.env.VITE_MOCK === "1" || import.meta.env.VITE_MOCK === "true";

/** The single shared client. Swap real vs mock at build/dev time via VITE_MOCK. */
export const api: ApiClient = useMock ? mockClient : httpClient;

export const IS_MOCK = useMock;

export * from "./types";
