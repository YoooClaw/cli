// Generate the metadata consumed by OpenHarmony's hnpcli.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [dir, version] = process.argv.slice(2);
if (!dir || !version) {
  console.error("用法: node scripts/gen-hnp.mjs <stage-dir> <version>");
  process.exit(1);
}

mkdirSync(dir, { recursive: true });
writeFileSync(
  join(dir, "hnp.json"),
  JSON.stringify(
    {
      type: "hnp-config",
      name: "yoooclaw",
      version,
      install: {
        links: [
          {
            source: "/bin/yoooclaw",
            target: "yoooclaw-native",
          },
        ],
      },
    },
    null,
    2,
  ) + "\n",
);
