import { afterEach, describe, expect, it } from "vitest";
import { PassThrough } from "node:stream";
import { mkdtempSync, lstatSync, rmSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildProgram } from "../src/program.js";
import { COMMAND_TREE } from "../src/command-tree.js";
import { renderError, renderResult, resolveFormat } from "../src/output/format.js";
import { YoooclawError } from "../src/errors.js";
import { resolveActiveProfile, type CliContext } from "../src/context.js";
import {
  resolveBundledSkillsDir,
  listBundledSkills,
  skillsTargets,
  skillsInstall,
} from "../src/commands/skills.js";

function capture(fn: (stream: PassThrough) => void): string {
  const stream = new PassThrough();
  const chunks: Buffer[] = [];
  stream.on("data", (c) => chunks.push(Buffer.from(c)));
  fn(stream);
  return Buffer.concat(chunks).toString("utf-8");
}

describe("command tree", () => {
  it("registers every service from the spec", () => {
    const program = buildProgram();
    const registered = program.commands.map((c) => c.name());
    for (const spec of COMMAND_TREE) {
      expect(registered).toContain(spec.name);
    }
  });

  it("exposes shortcuts as subcommands", () => {
    const program = buildProgram();
    const notification = program.commands.find((c) => c.name() === "notification");
    const subs = notification?.commands.map((c) => c.name()) ?? [];
    expect(subs).toContain("+today");
    expect(subs).toContain("+recent");
  });
});

describe("output format", () => {
  it("defaults to pretty on TTY and json off TTY", () => {
    expect(resolveFormat(undefined, true)).toBe("pretty");
    expect(resolveFormat(undefined, false)).toBe("json");
  });

  it("rejects unknown formats", () => {
    expect(() => resolveFormat("xml", true)).toThrow(YoooclawError);
  });

  it("renders ndjson one row per line", () => {
    const out = capture((stream) =>
      renderResult([{ a: 1 }, { a: 2 }], { format: "ndjson", stream }),
    );
    expect(out.trim().split("\n")).toEqual(['{"a":1}', '{"a":2}']);
  });

  it("renders errors with the unified ok:false schema", () => {
    const out = capture((stream) =>
      renderError(new YoooclawError("YOOOCLAW_NOT_FOUND", "nope"), {
        format: "json",
        stream,
      }),
    );
    expect(JSON.parse(out)).toEqual({
      ok: false,
      error: { code: "YOOOCLAW_NOT_FOUND", message: "nope" },
    });
  });
});

describe("skills install", () => {
  const ctx = {} as CliContext;
  let dirs: string[] = [];
  let envRestores: (() => void)[] = [];
  const tmpTarget = (): string => {
    const d = mkdtempSync(join(tmpdir(), "yc-skills-"));
    dirs.push(d);
    return d;
  };
  const setEnv = (name: string, value: string): void => {
    const previous = process.env[name];
    envRestores.push(() => {
      if (previous === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = previous;
      }
    });
    process.env[name] = value;
  };

  afterEach(() => {
    for (const restore of envRestores.reverse()) restore();
    envRestores = [];
    for (const d of dirs) rmSync(d, { recursive: true, force: true });
    dirs = [];
  });

  it("resolves the bundled skills dir and lists every SKILL.md", () => {
    const skillsDir = resolveBundledSkillsDir();
    const skills = listBundledSkills(skillsDir);
    expect(skills.length).toBeGreaterThan(0);
    const names = skills.map((s) => s.name);
    expect(names).toContain("yoooclaw-notification-query");
    // frontmatter 解析出 title/description
    const nq = skills.find((s) => s.name === "yoooclaw-notification-query");
    expect(nq?.title).toBe("yoooclaw-notification-query");
    expect(nq?.description.length).toBeGreaterThan(0);
  });

  it("symlinks each skill into the target", () => {
    const target = tmpTarget();
    const res = skillsInstall(ctx, [], { target }) as {
      ok: boolean;
      agent: string;
      mode: string;
      installed: string[];
    };
    expect(res.ok).toBe(true);
    expect(res.agent).toBe("custom");
    expect(res.mode).toBe("symlink");
    expect(res.installed).toContain("yoooclaw-notification-query");
    const link = join(target, "yoooclaw-notification-query");
    expect(lstatSync(link).isSymbolicLink()).toBe(true);
    expect(existsSync(join(link, "SKILL.md"))).toBe(true);
  });

  it("lists supported skill target adapters", () => {
    const res = skillsTargets(ctx) as {
      targets: { agent: string; target: string }[];
    };
    expect(res.targets.map((t) => t.agent)).toEqual(
      expect.arrayContaining(["claude", "codex"]),
    );
  });

  it("installs to the explicit codex agent default target", () => {
    const codexHome = tmpTarget();
    setEnv("CODEX_HOME", codexHome);
    const res = skillsInstall(ctx, [], { agent: "codex" }) as {
      agent: string;
      target: string;
      targetSource: string;
      installed: string[];
    };
    expect(res.agent).toBe("codex");
    expect(res.target).toBe(join(codexHome, "skills"));
    expect(res.targetSource).toBe("agent-default");
    expect(res.installed).toContain("yoooclaw-notification-query");
    expect(
      lstatSync(join(codexHome, "skills", "yoooclaw-notification-query")).isSymbolicLink(),
    ).toBe(true);
  });

  it("requires a target for the custom agent", () => {
    expect(() => skillsInstall(ctx, [], { agent: "custom" })).toThrow(YoooclawError);
  });

  it("is idempotent on a second symlink install (no skips)", () => {
    const target = tmpTarget();
    skillsInstall(ctx, [], { target });
    const res = skillsInstall(ctx, [], { target }) as {
      installed: string[];
      skipped: unknown[];
    };
    expect(res.skipped).toHaveLength(0);
    expect(res.installed).toContain("yoooclaw-notification-query");
  });

  it("skips existing on copy without --force, overwrites with --force", () => {
    const target = tmpTarget();
    skillsInstall(ctx, [], { target, copy: true });
    const blocked = skillsInstall(ctx, [], { target, copy: true }) as {
      installed: string[];
      skipped: { name: string }[];
    };
    expect(blocked.installed).toHaveLength(0);
    expect(blocked.skipped.length).toBeGreaterThan(0);

    const forced = skillsInstall(ctx, [], { target, copy: true, force: true }) as {
      installed: string[];
    };
    expect(forced.installed).toContain("yoooclaw-notification-query");
    expect(lstatSync(join(target, "yoooclaw-notification-query")).isDirectory()).toBe(true);
  });
});

describe("profile resolution", () => {
  it("prefers the explicit flag", () => {
    expect(resolveActiveProfile("work")).toBe("work");
  });

  it("falls back to default", () => {
    const prev = process.env.YOOOCLAW_PROFILE;
    const prevHome = process.env.YOOOCLAW_HOME;
    delete process.env.YOOOCLAW_PROFILE;
    process.env.YOOOCLAW_HOME = "/tmp/yoooclaw-test-nonexistent";
    expect(resolveActiveProfile(undefined)).toBe("default");
    if (prev !== undefined) process.env.YOOOCLAW_PROFILE = prev;
    if (prevHome !== undefined) process.env.YOOOCLAW_HOME = prevHome;
    else delete process.env.YOOOCLAW_HOME;
  });
});
