import { afterAll, describe, it } from "bun:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { REDACTED, SECRET_TOKEN } from "./contract";
import { evidence } from "./evidence";

const dirs: string[] = [];

afterAll(async () => {
  await Promise.all(dirs.map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("evidence", () => {
  it("never lands the secret on disk, whatever the binary printed", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-evidence-"));
    dirs.push(dir);
    await evidence(dir).write("up", "deploy.stdout", `set SECRET_TOKEN=${SECRET_TOKEN} ok\n`);
    const written = await readFile(path.join(dir, "up", "deploy.stdout"), "utf8");
    assert.ok(!written.includes(SECRET_TOKEN));
    assert.equal(written, `set SECRET_TOKEN=${REDACTED} ok\n`);
  });

  it("never lands a resource password on disk either", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-evidence-"));
    dirs.push(dir);
    await evidence(dir).write(
      "up",
      ".env",
      `OCEL_RESOURCE_POSTGRES_main={"name":"main","postgres":{"host":"localhost","password":"hunter2"}}\nDATABASE_URL=postgres://postgres:hunter2@localhost:5433/j-1\n`,
    );
    const written = await readFile(path.join(dir, "up", ".env"), "utf8");
    assert.ok(!written.includes("hunter2"), written);
    assert.ok(written.includes(`"password":"${REDACTED}"`), written);
    assert.ok(written.includes(`postgres://postgres:${REDACTED}@localhost:5433/j-1`), written);
  });
});
