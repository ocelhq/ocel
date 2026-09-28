import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { describe, it } from "node:test";
import { findLiveSuite } from "./live-suites.mjs";

const HOOK = join(import.meta.dirname, "live-suites.mjs");

function hook(command, env = {}) {
  const input = JSON.stringify({
    hook_event_name: "PreToolUse",
    tool_name: "Bash",
    tool_input: { command, description: "a command" },
  });
  const { OCEL_LIVE_LOCAL: _, ...inherited } = process.env;
  return spawnSync(process.execPath, [HOOK], {
    input,
    encoding: "utf8",
    env: { ...inherited, ...env },
  });
}

describe("findLiveSuite", () => {
  for (const [command, script] of [
    ["scripts/incus.sh run vps-1 -- go test ./...", "incus.sh"],
    ["./scripts/incus-fanout.sh lanes.tsv", "incus-fanout.sh"],
    ["scripts/floci.sh --cloud gcp run x -- go test ./...", "floci.sh"],
    ["scripts/act.sh go", "act.sh"],
    ["/home/me/ocel/scripts/act.sh journey", "act.sh"],
    ["bash scripts/act.sh", "act.sh"],
    ["cd scripts && ./floci.sh create x", "floci.sh"],
    ["pnpm install; scripts/incus.sh fetch", "incus.sh"],
    ["timeout 600 scripts/incus.sh run x -- true", "incus.sh"],
    ["FOO=bar scripts/floci.sh run x -- true", "floci.sh"],
    ['bash -c "scripts/incus.sh destroy x"', "incus.sh"],
    ["go build ./... && (scripts/act.sh go 2>&1 | tail)", "act.sh"],
    ["echo $(scripts/floci.sh status x)", "floci.sh"],
  ]) {
    it(`finds ${script} run by: ${command}`, () => {
      assert.equal(findLiveSuite(command), script);
    });
  }

  for (const command of [
    "cat scripts/incus.sh",
    "sed -n 1,40p scripts/act.sh",
    "grep -n floci scripts/floci.sh",
    "git diff origin/main -- scripts/incus-fanout.sh",
    'echo "run scripts/incus.sh in CI"',
    "go test ./...",
    "scripts/snapshot.mjs && scripts/preview-report.sh",
    "OCEL_LIVE_LOCAL=1 scripts/incus.sh run x -- go test ./...",
    "export OCEL_LIVE_LOCAL=1 && scripts/floci.sh run x -- go test ./...",
  ]) {
    it(`finds nothing in: ${command}`, () => {
      assert.equal(findLiveSuite(command), undefined);
    });
  }
});

describe("the hook", () => {
  it("blocks a live suite with exit 2 and says to push instead", () => {
    const result = hook("scripts/incus.sh run x -- go test ./...");
    assert.equal(result.status, 2);
    assert.match(result.stderr, /incus\.sh/);
    assert.match(result.stderr, /[Pp]ush/);
    assert.match(result.stderr, /OCEL_LIVE_LOCAL=1/);
  });

  it("lets any other command through", () => {
    const result = hook("go test ./...");
    assert.equal(result.status, 0);
    assert.equal(result.stderr, "");
  });

  it("lets a live suite through when OCEL_LIVE_LOCAL=1 is in its environment", () => {
    assert.equal(hook("scripts/act.sh go", { OCEL_LIVE_LOCAL: "1" }).status, 0);
  });

  it("lets through input that is not a Bash command", () => {
    const result = spawnSync(process.execPath, [HOOK], { input: "{}", encoding: "utf8" });
    assert.equal(result.status, 0);
  });
});
