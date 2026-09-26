import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { coveredNames, FRONT_ENV, frontNamed, frontStep, stepCommand, unrefused } from "./front";

describe("frontNamed", () => {
  it("runs a box behind ocel's own proxy when the run names no front", () => {
    expect(frontNamed({})).toBeUndefined();
    expect(frontNamed({ [FRONT_ENV]: " " })).toBeUndefined();
  });

  it("reads the proxy a front's box runs behind off its directory", () => {
    const front = frontNamed({ [FRONT_ENV]: "nginx" });
    expect(front?.proxy).toBe("manual");
    expect(frontStep(front!, "up.sh")).toContain("nginx");
  });

  it("refuses a front whose directory names no proxy for its projects", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "fronts-"));
    await mkdir(path.join(dir, "bare"));
    await writeFile(path.join(dir, "bare", "front.json"), "{}\n", "utf8");
    expect(() => frontNamed({ [FRONT_ENV]: "bare" }, dir)).toThrow(/names no "proxy"/);
  });

  it("reads what a bootstrap under ocel's own proxy must name as holding the ports", () => {
    expect(frontNamed({ [FRONT_ENV]: "nginx" })?.holder).toBe("nginx holds :80 and :443");
    expect(frontNamed({ [FRONT_ENV]: "nginx-container" })?.holder).toBe(
      "container ocel-front-nginx publishes :80 and :443",
    );
  });

  it("refuses a front whose directory names nothing holding the ports", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "fronts-"));
    await mkdir(path.join(dir, "unheld"));
    await writeFile(path.join(dir, "unheld", "front.json"), '{ "proxy": "manual" }\n', "utf8");
    expect(() => frontNamed({ [FRONT_ENV]: "unheld" }, dir)).toThrow(/names no "holder"/);
  });
});

describe("unrefused", () => {
  const holder = "nginx holds :80 and :443";
  const refused =
    '✗ Failed\n  not_ready: nginx holds :80 and :443, where ocel\'s own proxy serves\n  Add `"proxy": "manual"` ...\n';

  it("passes a bootstrap that refused naming what holds the ports", () => {
    expect(unrefused(1, refused, holder)).toBeUndefined();
  });

  it("reads the refusal through the colour a terminal paints it in", () => {
    expect(unrefused(1, `\u001b[31m${refused}\u001b[0m`, holder)).toBeUndefined();
  });

  it("fails a bootstrap that went ahead over a box whose ports are held", () => {
    expect(unrefused(0, "Bootstrapped production\n", holder)).toContain("went ahead");
  });

  it("fails a refusal that never names what holds the ports", () => {
    expect(unrefused(1, "✗ Failed\n  denied: no route to host\n", holder)).toContain(holder);
  });
});

describe("stepCommand", () => {
  it("hands a step the names its certificate covers, quoted for the box's shell", () => {
    expect(stepCommand(coveredNames("localhost"))).toBe("sudo sh -s -- '*.localhost'");
    expect(stepCommand(["it's"])).toBe(`sudo sh -s -- 'it'\\''s'`);
  });
});
