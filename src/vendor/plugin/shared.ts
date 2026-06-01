import type { IncomingMessage } from "node:http";
import type { OpenClawPluginApi } from "openclaw/plugin-sdk";

export type GatewayHandler = Parameters<OpenClawPluginApi["registerGatewayMethod"]>[1];

export type GatewayMethodRegistrar = (
  method: string,
  handler: GatewayHandler,
) => void;

export function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks).toString()));
    req.on("error", reject);
  });
}

export function trimToUndefined(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }

  const trimmed = value.trim();
  return trimmed || undefined;
}
