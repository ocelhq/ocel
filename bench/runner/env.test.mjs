import { describe, expect, it } from "vitest";

import { loadEnvFile, parseEnvFile } from "./env.mjs";

describe("parseEnvFile", () => {
  it("reads names, values, quotes and comments", () => {
    expect(
      parseEnvFile(["# a comment", "", "A=1", 'B="two words"', "C='three'", "D=has=equals", "  E = spaced  "].join("\n")),
    ).toEqual({ A: "1", B: "two words", C: "three", D: "has=equals", E: "spaced" });
  });

  it("ignores a line with no name", () => {
    expect(parseEnvFile("=orphan\nA=1")).toEqual({ A: "1" });
  });
});

describe("loadEnvFile", () => {
  it("returns nothing for a file that is not there", () => {
    expect(loadEnvFile("/definitely/not/a/path/.env.local", {})).toEqual([]);
  });

  it("never overwrites a value already in the environment", () => {
    const env = { A: "from-the-shell" };
    const path = new URL("./env.test.fixture", import.meta.url);
    expect(loadEnvFile(path.pathname, env)).toEqual(["B"]);
    expect(env.A).toBe("from-the-shell");
    expect(env.B).toBe("only-in-the-file");
  });
});
