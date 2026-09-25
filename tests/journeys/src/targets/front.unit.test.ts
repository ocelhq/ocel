import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { coveredNames, FRONT_ENV, frontNamed, frontStep, stepCommand } from "./front";

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
});

describe("stepCommand", () => {
  it("hands a step the names its certificate covers, quoted for the box's shell", () => {
    expect(stepCommand(coveredNames("localhost"))).toBe("sudo sh -s -- '*.localhost'");
    expect(stepCommand(["it's"])).toBe(`sudo sh -s -- 'it'\\''s'`);
  });
});
