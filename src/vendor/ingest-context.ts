import { AsyncLocalStorage } from "node:async_hooks";

export interface IngestContext {
  clientLabel?: string;
  authKind?: "relay-api-key" | "http-api-key" | "gateway-token" | "local" | string;
}

const storage = new AsyncLocalStorage<IngestContext>();

export function runWithIngestContext<T>(context: IngestContext | undefined, fn: () => T): T {
  return storage.run(context ?? {}, fn);
}

export function getIngestContext(): IngestContext {
  return storage.getStore() ?? {};
}

export function currentClientLabel(fallback = "legacy"): string {
  return getIngestContext().clientLabel?.trim() || fallback;
}
