import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import { resolveStateDir } from "../host.js";

// ─── Types ───

export interface DeviceIdentity {
  deviceId: string;
  publicKeyPem: string;
  privateKeyPem: string;
}

export interface DeviceAuthEntry {
  token: string;
  role: string;
  scopes: string[];
  updatedAtMs: number;
}

interface DeviceAuthStore {
  version: 1;
  deviceId: string;
  tokens: Record<string, DeviceAuthEntry>;
}

// ─── Crypto helpers ───

const ED25519_SPKI_PREFIX = Buffer.from("302a300506032b6570032100", "hex");

export function base64UrlEncode(buf: Buffer): string {
  return buf
    .toString("base64")
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/g, "");
}

export function derivePublicKeyRaw(publicKeyPem: string): Buffer {
  const spki = crypto.createPublicKey(publicKeyPem).export({
    type: "spki",
    format: "der",
  });
  if (
    spki.length === ED25519_SPKI_PREFIX.length + 32 &&
    spki.subarray(0, ED25519_SPKI_PREFIX.length).equals(ED25519_SPKI_PREFIX)
  )
    return spki.subarray(ED25519_SPKI_PREFIX.length);
  return spki;
}

export function fingerprintPublicKey(publicKeyPem: string): string {
  const raw = derivePublicKeyRaw(publicKeyPem);
  return crypto.createHash("sha256").update(raw).digest("hex");
}

export function publicKeyRawBase64UrlFromPem(publicKeyPem: string): string {
  return base64UrlEncode(derivePublicKeyRaw(publicKeyPem));
}

export function signDevicePayload(privateKeyPem: string, payload: string): string {
  const key = crypto.createPrivateKey(privateKeyPem);
  return base64UrlEncode(crypto.sign(null, Buffer.from(payload, "utf8"), key));
}

export function buildDeviceAuthPayload(params: {
  deviceId: string;
  clientId: string;
  clientMode: string;
  role: string;
  scopes: string[];
  signedAtMs: number;
  token: string | null;
  nonce: string;
}): string {
  const scopes = params.scopes.join(",");
  const token = params.token ?? "";
  return [
    "v2",
    params.deviceId,
    params.clientId,
    params.clientMode,
    params.role,
    scopes,
    String(params.signedAtMs),
    token,
    params.nonce,
  ].join("|");
}

// ─── File I/O helpers ───

export function resolveClientStateDir(stateDir: string | undefined): string {
  return stateDir ?? resolveStateDir();
}

function ensureDir(filePath: string): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
}

function resolveIdentityPath(stateDir: string): string {
  return path.join(stateDir, "identity", "device.json");
}

function normalizeDeviceAuthRole(role: string): string {
  return role.trim();
}

function normalizeDeviceAuthScopes(scopes: string[]): string[] {
  const out = new Set<string>();
  for (const scope of scopes) {
    const trimmed = scope.trim();
    if (trimmed) {
      out.add(trimmed);
    }
  }
  return [...out].sort();
}

function resolveDeviceAuthPath(stateDir: string): string {
  return path.join(stateDir, "identity", "device-auth.json");
}

function readDeviceAuthStore(filePath: string): DeviceAuthStore | null {
  try {
    if (!fs.existsSync(filePath)) return null;
    const raw = fs.readFileSync(filePath, "utf8");
    const parsed = JSON.parse(raw);
    if (parsed?.version !== 1 || typeof parsed.deviceId !== "string") return null;
    if (!parsed.tokens || typeof parsed.tokens !== "object") return null;
    return parsed as DeviceAuthStore;
  } catch {
    return null;
  }
}

function writeDeviceAuthStore(filePath: string, store: DeviceAuthStore): void {
  ensureDir(filePath);
  fs.writeFileSync(filePath, `${JSON.stringify(store, null, 2)}\n`, {
    mode: 0o600,
  });
  try {
    fs.chmodSync(filePath, 0o600);
  } catch {
    // ignore chmod errors on unsupported filesystems
  }
}

// ─── Token CRUD ───

export function loadDeviceAuthToken(params: {
  stateDir: string;
  deviceId: string;
  role: string;
}): DeviceAuthEntry | null {
  const store = readDeviceAuthStore(resolveDeviceAuthPath(params.stateDir));
  if (!store || store.deviceId !== params.deviceId) return null;
  const entry = store.tokens[normalizeDeviceAuthRole(params.role)];
  if (!entry || typeof entry.token !== "string") return null;
  return entry;
}

export function storeDeviceAuthToken(params: {
  stateDir: string;
  deviceId: string;
  role: string;
  token: string;
  scopes: string[];
}): DeviceAuthEntry {
  const filePath = resolveDeviceAuthPath(params.stateDir);
  const existing = readDeviceAuthStore(filePath);
  const role = normalizeDeviceAuthRole(params.role);
  const next: DeviceAuthStore = {
    version: 1,
    deviceId: params.deviceId,
    tokens:
      existing && existing.deviceId === params.deviceId && existing.tokens
        ? { ...existing.tokens }
        : {},
  };
  const entry: DeviceAuthEntry = {
    token: params.token,
    role,
    scopes: normalizeDeviceAuthScopes(params.scopes),
    updatedAtMs: Date.now(),
  };
  next.tokens[role] = entry;
  writeDeviceAuthStore(filePath, next);
  return entry;
}

export function clearDeviceAuthToken(params: {
  stateDir: string;
  deviceId: string;
  role: string;
}): void {
  const filePath = resolveDeviceAuthPath(params.stateDir);
  const store = readDeviceAuthStore(filePath);
  if (!store || store.deviceId !== params.deviceId) return;
  const role = normalizeDeviceAuthRole(params.role);
  if (!store.tokens[role]) return;
  const next: DeviceAuthStore = {
    version: 1,
    deviceId: store.deviceId,
    tokens: { ...store.tokens },
  };
  delete next.tokens[role];
  writeDeviceAuthStore(filePath, next);
}

// ─── Identity lifecycle ───

export function loadOrCreateDeviceIdentity(stateDir: string): DeviceIdentity {
  const filePath = resolveIdentityPath(stateDir);
  try {
    if (fs.existsSync(filePath)) {
      const raw = fs.readFileSync(filePath, "utf8");
      const parsed = JSON.parse(raw);
      if (
        parsed?.version === 1 &&
        typeof parsed.deviceId === "string" &&
        typeof parsed.publicKeyPem === "string" &&
        typeof parsed.privateKeyPem === "string"
      ) {
        const derivedId = fingerprintPublicKey(parsed.publicKeyPem);
        return {
          deviceId: derivedId,
          publicKeyPem: parsed.publicKeyPem,
          privateKeyPem: parsed.privateKeyPem,
        };
      }
    }
  } catch {
    // fall through to generate
  }
  // Generate new identity
  const { publicKey, privateKey } = crypto.generateKeyPairSync("ed25519");
  const publicKeyPem = publicKey
    .export({ type: "spki", format: "pem" })
    .toString();
  const privateKeyPem = privateKey
    .export({ type: "pkcs8", format: "pem" })
    .toString();
  const identity: DeviceIdentity = {
    deviceId: fingerprintPublicKey(publicKeyPem),
    publicKeyPem,
    privateKeyPem,
  };
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  const stored = {
    version: 1,
    ...identity,
    createdAtMs: Date.now(),
  };
  ensureDir(filePath);
  fs.writeFileSync(filePath, `${JSON.stringify(stored, null, 2)}\n`, {
    mode: 0o600,
  });
  return identity;
}
