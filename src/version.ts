import { readFileSync } from "node:fs";

declare const __CLI_VERSION__: string | undefined;
declare const __CLI_DIST__: string | undefined;

interface PackageJson {
  version?: string;
}

function readBuildInjectedVersion(): string | undefined {
  if (typeof __CLI_VERSION__ !== "string") {
    return undefined;
  }
  const version = __CLI_VERSION__.trim();
  return version || undefined;
}

function readVersionFromPackageJson(): string | undefined {
  try {
    const packageJsonUrl = new URL("../package.json", import.meta.url);
    const packageJson = JSON.parse(
      readFileSync(packageJsonUrl, "utf-8"),
    ) as PackageJson;
    const version = packageJson.version?.trim();
    return version || undefined;
  } catch {
    return undefined;
  }
}

export const CLI_VERSION =
  readBuildInjectedVersion() ?? readVersionFromPackageJson() ?? "unknown";

export type CliDist = "npm" | "native" | "dev";

function readBuildInjectedDist(): CliDist | undefined {
  if (typeof __CLI_DIST__ !== "string") return undefined;
  const value = __CLI_DIST__.trim();
  if (value === "npm" || value === "native" || value === "dev") return value;
  return undefined;
}

export const CLI_DIST: CliDist = readBuildInjectedDist() ?? "dev";
