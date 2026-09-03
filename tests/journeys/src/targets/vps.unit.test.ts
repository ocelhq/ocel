import { describe, expect, it } from "vitest";
import { REDACTED } from "../contract";
import {
  boxEnvironment,
  issuedByTheBox,
  journeyZone,
  recordFile,
  slugsOf,
  ssh,
  strandedSlugs,
} from "./vps";

const IDENTITY = "/nonexistent/ocel-journey-identity";

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

describe("journeyZone", () => {
  it("serves the box's own names when the caller named no zone", () => {
    expect(journeyZone({})).toBe("localhost");
    expect(journeyZone({ OCEL_JOURNEY_ZONE: "  " })).toBe("localhost");
  });

  it("keeps the zone the caller named", () => {
    expect(journeyZone({ OCEL_JOURNEY_ZONE: "journeys.example" })).toBe("journeys.example");
  });
});

describe("issuedByTheBox", () => {
  it("reads a localhost zone as one the box issues for itself", () => {
    expect(issuedByTheBox("localhost")).toBe(true);
    expect(issuedByTheBox("boxes.localhost")).toBe(true);
  });

  it("leaves a public zone to the certificate authority that answers for it", () => {
    expect(issuedByTheBox("journeys.example")).toBe(false);
    expect(issuedByTheBox("notlocalhost")).toBe(false);
  });
});

describe("recordFile", () => {
  it("names the record a slug of plain characters is kept in", () => {
    expect(recordFile("j-local-ada-express")).toBe("j-local-ada-express.rec");
  });

  it("percent-encodes what the records tier cannot keep in a file name", () => {
    expect(recordFile("j-local-ada/express")).toBe("j-local-ada%2Fexpress.rec");
  });

  it("encodes a leading dot so no record hides itself", () => {
    expect(recordFile(".hidden")).toBe("%2Ehidden.rec");
  });
});

describe("slugsOf", () => {
  it("names one project per record the box holds", () => {
    expect(slugsOf("/records/projects/production/j-local-ada-express.rec\n")).toEqual([
      "j-local-ada-express",
    ]);
  });

  it("ignores anything that is not a record", () => {
    expect(slugsOf("\nno-records-here\n")).toEqual([]);
  });

  it("reads back a slug the records tier percent-encoded", () => {
    expect(slugsOf("j-local-ada%2Fexpress.rec")).toEqual(["j-local-ada/express"]);
  });

  it("refuses to read a box with no records tier as a box holding nothing", () => {
    expect(() => slugsOf("no-records-tier\n")).toThrow(
      /records\/projects\/production does not exist/,
    );
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

describe("ssh", () => {
  it("names no identity file in what it throws when the box does not answer", async () => {
    const thrown = await ssh(
      { host: "box.invalid", user: "nobody", identityFile: IDENTITY },
      "nobody",
      "true",
    ).then(
      () => new Error("the box answered, and nothing here can reach box.invalid"),
      (error: Error) => error,
    );
    expect(thrown.message).not.toContain(IDENTITY);
    expect(thrown.message).toContain(REDACTED);
    expect(thrown.message).toContain("nobody@box.invalid");
  });
});
