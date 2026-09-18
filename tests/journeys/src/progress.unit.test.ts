import { afterAll, describe, it } from "bun:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { PassThrough } from "node:stream";
import { REDACTED, SECRET_TOKEN } from "./checks/context";
import { follow, lines, PROGRESS_ENV, progress, relay } from "./progress";

const dirs: string[] = [];

afterAll(async () => {
  await Promise.all(dirs.map((dir) => rm(dir, { recursive: true, force: true })));
});

async function scratch(): Promise<string> {
  const dir = await mkdtemp(path.join(tmpdir(), "journey-progress-"));
  dirs.push(dir);
  return dir;
}

describe("progress", () => {
  it("appends a prefixed, redacted line to the file the lane named", async () => {
    const file = path.join(await scratch(), "progress.log");
    const log = progress("cell deploy/deploy", { [PROGRESS_ENV]: file });
    log(`token ${SECRET_TOKEN} set`);
    log("done");
    assert.equal(
      await readFile(file, "utf8"),
      `cell deploy/deploy token ${REDACTED} set\ncell deploy/deploy done\n`,
    );
  });

  it("splits chunks into lines and flushes the tail at the end", () => {
    const seen: string[] = [];
    const split = lines((line) => seen.push(line));
    split.push("one\ntw");
    split.push("o\r\nthree");
    assert.deepEqual(seen, ["one", "two"]);
    split.end();
    assert.deepEqual(seen, ["one", "two", "three"]);
  });

  it("relays a stream line by line as it arrives", async () => {
    const seen: string[] = [];
    const stream = new PassThrough();
    relay(stream, (line) => seen.push(line));
    stream.write("deploying\npro");
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(seen, ["deploying"]);
    stream.end("visioned\n");
    await new Promise((resolve) => stream.on("end", resolve));
    assert.deepEqual(seen, ["deploying", "provisioned"]);
  });

  it("follows what workers append and drains the rest on stop", async () => {
    const file = path.join(await scratch(), "progress.log");
    const out: string[] = [];
    const stop = follow(file, (text) => out.push(String(text)), 10);
    await writeFile(file, "first\n", "utf8");
    await new Promise((resolve) => setTimeout(resolve, 40));
    assert.deepEqual(out, ["first\n"]);
    progress("late", { [PROGRESS_ENV]: file })("second");
    stop();
    assert.equal(out.join(""), "first\nlate second\n");
  });
});
