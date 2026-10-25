import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runBindings } from "./cli.js";

vi.mock("node:child_process", () => {
  const spawnSync = vi.fn();
  return { spawnSync, default: { spawnSync } };
});

const run = vi.mocked(spawnSync);

const target = { project: ".", tier: "production" } as const;

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "ocel-pulumi-cli-"));
}

function onPath(): string {
  const dir = scratch();
  const ocel = join(dir, "ocel");
  writeFileSync(ocel, "#!/bin/sh\n");
  chmodSync(ocel, 0o755);
  return ocel;
}

function withCLIPackage(bin: string): string {
  const project = scratch();
  const root = join(project, "node_modules", "@ocel", "cli");
  mkdirSync(join(root, "bin"), { recursive: true });
  writeFileSync(
    join(root, "package.json"),
    JSON.stringify({ name: "@ocel/cli", bin: { ocel: bin } }),
  );
  writeFileSync(join(root, bin), "");
  writeFileSync(join(project, "ocel.json"), "{}");
  return project;
}

function goOnlyProject(): string {
  const project = scratch();
  writeFileSync(join(project, "ocel.json"), "{}");
  return project;
}

beforeEach(() => {
  run.mockReturnValue({ status: 0, stderr: "" } as never);
  vi.stubEnv("OCEL_CLI", "");
  vi.stubEnv("PATH", scratch());
});

afterEach(() => {
  vi.clearAllMocks();
  vi.unstubAllEnvs();
});

function spawned() {
  const call = run.mock.calls[0];
  if (!call) throw new Error("the ocel CLI was never run");
  return { command: call[0], args: call[1] as string[], options: call[2] };
}

describe("reaching the ocel CLI", () => {
  it("runs the CLI $OCEL_CLI names before any other", () => {
    vi.stubEnv("OCEL_CLI", "/opt/ocel/bin/ocel");
    vi.stubEnv("PATH", [scratch(), join(onPath(), "..")].join(delimiter));
    const project = withCLIPackage("bin/ocel.js");

    runBindings(["ls"], target, project);

    expect(spawned()).toMatchObject({
      command: "/opt/ocel/bin/ocel",
      args: ["bindings", "ls"],
      options: { cwd: project },
    });
  });

  it("runs the ocel on PATH in a project that installs no ocel package", () => {
    const ocel = onPath();
    vi.stubEnv("PATH", [scratch(), join(ocel, "..")].join(delimiter));
    const project = goOnlyProject();

    runBindings(["ls"], target, project);

    expect(spawned()).toMatchObject({
      command: ocel,
      args: ["bindings", "ls"],
      options: { cwd: project },
    });
  });

  it("runs the bin the project's own @ocel/cli ships when nothing else names a CLI", () => {
    const project = withCLIPackage("bin/ocel.js");

    runBindings(["ls"], target, project);

    expect(spawned()).toMatchObject({
      command: process.execPath,
      args: [join(project, "node_modules", "@ocel", "cli", "bin", "ocel.js"), "bindings", "ls"],
    });
  });

  it("names the three places it looked when it finds no CLI", () => {
    const project = goOnlyProject();

    expect(() => runBindings(["ls"], target, project)).toThrow(
      `@ocel/pulumi publishes bindings with the ocel CLI and found none: set $OCEL_CLI to its path, put ocel on PATH, or add @ocel/cli to the dependencies of ${project}`,
    );
    expect(run).not.toHaveBeenCalled();
  });

  it("says so when the CLI dies without a word", () => {
    vi.stubEnv("OCEL_CLI", "/opt/ocel/bin/ocel");
    run.mockReturnValue({ status: 3, stderr: "  \n" } as never);

    expect(() => runBindings(["ls"], target, goOnlyProject())).toThrow(
      /exited 3 without saying why/,
    );
  });

  it("refuses a target no binding could be published to before spawning anything", () => {
    vi.stubEnv("OCEL_CLI", "/opt/ocel/bin/ocel");

    expect(() => runBindings(["ls"], { ...target, project: "" }, goOnlyProject())).toThrow(
      /an ocel project is required/,
    );
    expect(run).not.toHaveBeenCalled();
  });
});
