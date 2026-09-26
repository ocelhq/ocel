import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, readdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  coveredNames,
  FRONT_ENV,
  frontNamed,
  frontStep,
  frontsDir,
  holderOf,
  refusalMissed,
  stepCommand,
} from "./front";

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

  it("reads a front's directory for its proxy option and nothing else", async () => {
    for (const entry of await readdir(frontsDir, { withFileTypes: true })) {
      if (!entry.isDirectory()) {
        continue;
      }
      const read = JSON.parse(
        await readFile(path.join(frontsDir, entry.name, "front.json"), "utf8"),
      );
      expect(Object.keys(read)).toEqual(["proxy"]);
    }
  });
});

describe("holderOf", () => {
  it("names what holds the ports on each front this repo carries", () => {
    expect(holderOf(frontNamed({ [FRONT_ENV]: "nginx" })!)).toBe("nginx holds :80 and :443");
    expect(holderOf(frontNamed({ [FRONT_ENV]: "nginx-container" })!)).toBe(
      "container ocel-front-nginx publishes :80 and :443",
    );
    expect(holderOf(frontNamed({ [FRONT_ENV]: "nginx-network" })!)).toBe(
      "container ocel-front-nginx-network publishes :80 and :443",
    );
  });

  it("knows what holds the ports on every front directory the lanes can name", async () => {
    for (const entry of await readdir(frontsDir, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        expect(() => holderOf(frontNamed({ [FRONT_ENV]: entry.name })!)).not.toThrow();
      }
    }
  });

  it("refuses a front the journey knows nothing holding the ports for", () => {
    expect(() => holderOf({ name: "unheld", dir: "/nowhere", proxy: "manual" })).toThrow(/unheld/);
  });
});

describe("refusalMissed", () => {
  const holder = "nginx holds :80 and :443";
  const refused =
    '✗ Failed\n  not_ready: nginx holds :80 and :443, where ocel\'s own proxy serves\n  Add `"proxy": "manual"` ...\n';

  it("passes a bootstrap that refused naming what holds the ports", () => {
    expect(refusalMissed(1, refused, holder)).toBeUndefined();
  });

  it("reads the refusal through the colour a terminal paints it in", () => {
    expect(refusalMissed(1, `\u001b[31m${refused}\u001b[0m`, holder)).toBeUndefined();
  });

  it("fails a bootstrap that went ahead over a box whose ports are held", () => {
    expect(refusalMissed(0, "Bootstrapped production\n", holder)).toContain("went ahead");
  });

  it("fails a refusal that never names what holds the ports", () => {
    expect(refusalMissed(1, "✗ Failed\n  denied: no route to host\n", holder)).toContain(holder);
  });
});

describe("stepCommand", () => {
  it("hands a step the names its certificate covers, quoted for the box's shell", () => {
    expect(stepCommand(coveredNames("localhost"))).toBe("sudo sh -s -- '*.localhost'");
    expect(stepCommand(["it's"])).toBe(`sudo sh -s -- 'it'\\''s'`);
  });
});
