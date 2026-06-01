import { loadApiKey } from "../auth/credentials.js";
import { getEnvUrls } from "../env.js";

export type GatewayConnectStatus = "connected" | "disconnected";

interface Logger {
  info: (msg: string) => void;
  warn: (msg: string) => void;
}

export interface ConnectStatusReporter {
  report(status: GatewayConnectStatus): void;
  reset(): void;
}

export function createConnectStatusReporter(opts: {
  logger: Logger;
}): ConnectStatusReporter {
  const { logger } = opts;
  let lastReported: GatewayConnectStatus | null = null;
  let inFlight: Promise<void> | null = null;

  async function post(status: GatewayConnectStatus): Promise<void> {
    const url = getEnvUrls().gatewayConnectStatusUrl;
    if (!url) {
      logger.warn(
        "[gateway-connect-status] url is empty for current env, skip report",
      );
      return;
    }
    const apiKey = loadApiKey();
    if (!apiKey) {
      logger.info(
        "[gateway-connect-status] api key missing, skip report",
      );
      return;
    }
    const rawApiKey = apiKey.startsWith("Bearer ")
      ? apiKey.slice("Bearer ".length)
      : apiKey;
    try {
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ apiKey: rawApiKey, connectStatus: status }),
      });
      if (!res.ok) {
        logger.warn(
          `[gateway-connect-status] report ${status} failed: HTTP ${res.status} ${res.statusText}`,
        );
        return;
      }
      logger.info(`[gateway-connect-status] reported ${status}`);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      logger.warn(`[gateway-connect-status] report ${status} error: ${message}`);
    }
  }

  return {
    report(status: GatewayConnectStatus): void {
      if (lastReported === status) return;
      lastReported = status;
      const run = async () => {
        if (inFlight) {
          try {
            await inFlight;
          } catch {
            // ignore
          }
        }
        await post(status);
      };
      inFlight = run().finally(() => {
        inFlight = null;
      });
    },
    reset(): void {
      lastReported = null;
    },
  };
}
