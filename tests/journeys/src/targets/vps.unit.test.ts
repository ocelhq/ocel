import { describe, expect, it } from "vitest";
import { boxEnvironment, slugsOf, strandedSlugs } from "./vps";

describe("boxEnvironment", () => {
  it("reads the incus marker as the emulator environment", () => {
    expect(boxEnvironment("incus\n")).toBe("vps.incus");
  });

  it("reads a box without the marker as a real one", () => {
    expect(boxEnvironment("real\n")).toBe("vps");
  });

  it("refuses an answer it cannot read rather than guessing a box is disposable", () => {
    expect(() => boxEnvironment("")).toThrow(/whether it runs under incus/);
  });
});

describe("slugsOf", () => {
  it("names one project per record the box holds", () => {
    expect(slugsOf("j-local-ada-express.rec\nocelhq.rec\n")).toEqual([
      "j-local-ada-express",
      "ocelhq",
    ]);
  });

  it("ignores anything that is not a record", () => {
    expect(slugsOf("\nno-records-here\n")).toEqual([]);
  });

  it("reads back a slug the records tier percent-encoded", () => {
    expect(slugsOf("j-local-ada%2Fexpress.rec")).toEqual(["j-local-ada/express"]);
  });
});

describe("strandedSlugs", () => {
  const slugs = ["ocelhq", "j-local-ada-express", "j-42-express", "j-4242-express"];

  it("leaves projects that are not the harness's alone", () => {
    expect(strandedSlugs(slugs, "42")).not.toContain("ocelhq");
  });

  it("leaves this run's own projects standing", () => {
    expect(strandedSlugs(slugs, "42")).toEqual(["j-local-ada-express", "j-4242-express"]);
  });

  it("takes every harness project when the run identity matches none", () => {
    expect(strandedSlugs(slugs, "local-bob")).toEqual([
      "j-local-ada-express",
      "j-42-express",
      "j-4242-express",
    ]);
  });
});
