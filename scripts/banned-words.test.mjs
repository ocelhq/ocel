import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { hasBannedWord } from "./banned-words.mjs";

const script = fileURLToPath(new URL("./banned-words.mjs", import.meta.url));

function runInRepository(files) {
  const repository = mkdtempSync(join(tmpdir(), "banned-words-"));
  try {
    for (const [path, text] of Object.entries(files)) {
      mkdirSync(join(repository, dirname(path)), { recursive: true });
      writeFileSync(join(repository, path), text);
    }
    execFileSync("git", ["init", "-q"], { cwd: repository });
    execFileSync("git", ["add", "."], { cwd: repository });
    return spawnSync(process.execPath, [script], { cwd: repository, encoding: "utf8" });
  } finally {
    rmSync(repository, { recursive: true, force: true });
  }
}

describe("hasBannedWord", () => {
  it("is true for a banned word on its own, in capitals, or as a camelCase part", () => {
    for (const line of [
      "the standing config",
      "CERT_HELD",
      "func heldCert()",
      "isSettled := true",
      "TestURLSettlesTheEndpoint",
      "TestURLCarriesTheEndpoint",
      "carryRequestQuery(headers)",
      "a bug fix carries a test",
      "carrying the query",
    ]) {
      assert.equal(hasBannedWord(line), true, line);
    }
  });

  it("is false for a word that only contains a banned word", () => {
    for (const line of ["Outstanding", "a standard stand-in", "standalone", "withheld"]) {
      assert.equal(hasBannedWord(line), false, line);
    }
  });

  it("is false for a lock held, in any case", () => {
    for (const line of [
      "with the lock held",
      "while the locks held",
      "const LOCK_HELD = 1",
      "a deploy held the lock",
      "on a held dpkg lock",
      "a request held open across the flip",
    ]) {
      assert.equal(hasBannedWord(line), false, line);
    }
  });

  it("is false for ECMAScript's settled promise", () => {
    for (const line of [
      "await Promise.allSettled(tasks)",
      "PromiseSettledResult<void>",
      "settledWithin(promise, ms)",
      "settledPool(n, limit, run)",
    ]) {
      assert.equal(hasBannedWord(line), false, line);
    }
  });

  it("is false for a persisted JSON tag, with or without options", () => {
    for (const line of ['Amount int `json:"owed"`', 'Host State `json:"settled,omitzero"`']) {
      assert.equal(hasBannedWord(line), false, line);
    }
  });

  it("is true for a banned word beside an allowed phrase on the same line", () => {
    for (const line of [
      "held := s.lock.TryLock()",
      "standing, _ := Promise.allSettled(x)",
      'Owed []R `json:"owed"`',
    ]) {
      assert.equal(hasBannedWord(line), true, line);
    }
  });
});

describe("banned-words.mjs", () => {
  it("exits 0 when no tracked file or path uses a banned word", () => {
    const run = runInRepository({ "main.go": "package main\n" });
    assert.equal(run.status, 0, run.stdout);
  });

  it("exits 1 and names the line of a banned word in a tracked file", () => {
    const run = runInRepository({ "main.go": "package main\n\nvar standing = 1\n" });
    assert.equal(run.status, 1);
    assert.match(run.stdout, /^main\.go:3:var standing = 1$/m);
  });

  it("exits 1 and names a tracked path that uses a banned word", () => {
    const run = runInRepository({ "pkg/held/held.go": "package cert\n" });
    assert.equal(run.status, 1);
    assert.match(run.stdout, /^pkg\/held\/held\.go$/m);
  });

  it("exits 0 when the banned word is only in a generated file, a lockfile or a license", () => {
    const run = runInRepository({
      "gen/held.go": "var standing = 1\n",
      "pnpm-lock.yaml": "settled: true\n",
      "go.sum": "owed v1.0.0\n",
      LICENSE: "You must cause any modified files to carry prominent notices\n",
    });
    assert.equal(run.status, 0, run.stdout);
  });
});
