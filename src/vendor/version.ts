import { readFileSync } from "node:fs";

declare const __PLUGIN_VERSION__: string | undefined;

interface PackageJson {
  version?: string;
}

function readBuildInjectedVersion(): string | undefined {
  if (typeof __PLUGIN_VERSION__ !== "string") {
    return undefined;
  }

  const version = __PLUGIN_VERSION__.trim();
  return version || undefined;
}

function readPluginVersionFromPackageJson(): string | undefined {
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

export const PLUGIN_VERSION =
  readBuildInjectedVersion() ??
  readPluginVersionFromPackageJson() ??
  "unknown";
