import { describe, expect, it } from "bun:test";
import { REDACTED } from "../checks/context";
import { boxLane, hostnamesWithoutUrl, recordFile, slugsOf, ssh } from "./vps";

const IDENTITY = "/nonexistent/ocel-journey-identity";

describe("boxLane", () => {
  it("reads the incus marker as the emulator lane", () => {
    expect(boxLane("incus\n")).toBe("vps.incus");
  });

  it("reads a box without the marker as a real one", () => {
    expect(boxLane("real\n")).toBe("vps");
  });

  it("refuses an answer it cannot read rather than guessing a box is disposable", () => {
    expect(() => boxLane("")).toThrow(/whether it runs under incus/);
  });
});

describe("hostnamesWithoutUrl", () => {
  it("passes a deploy that printed a url for every hostname it declares", () => {
    const said = "Done in 1m\n\n    web  https://web-j-1-deploy-python.localhost\n";
    expect(hostnamesWithoutUrl(said, ["web-j-1-deploy-python.localhost"])).toEqual([]);
  });

  it("names the hostnames the deploy left pending", () => {
    const said =
      "Done in 2m\n\n    web-j-1-deploy-python.localhost does not answer as the box edge yet — `ocel domain add` picks up where it stopped\n";
    expect(hostnamesWithoutUrl(said, ["web-j-1-deploy-python.localhost"])).toEqual([
      "web-j-1-deploy-python.localhost",
    ]);
  });

  it("does not read one hostname's url as another's that it prefixes", () => {
    const said = "    web  https://web.localhost.example\n";
    expect(hostnamesWithoutUrl(said, ["web.localhost"])).toEqual(["web.localhost"]);
  });
});

describe("recordFile", () => {
  it("names the record a slug of plain characters is kept in", () => {
    expect(recordFile("j-local-ada-node")).toBe("j-local-ada-node.rec");
  });

  it("percent-encodes what the records tier cannot keep in a file name", () => {
    expect(recordFile("j-local-ada/node")).toBe("j-local-ada%2Fnode.rec");
  });

  it("encodes a leading dot so no record hides itself", () => {
    expect(recordFile(".hidden")).toBe("%2Ehidden.rec");
  });
});

describe("slugsOf", () => {
  it("names one project per record the box stores", () => {
    expect(slugsOf("/records/projects/production/j-local-ada-node.rec\n")).toEqual([
      "j-local-ada-node",
    ]);
  });

  it("ignores anything that is not a record", () => {
    expect(slugsOf("\nno-records-here\n")).toEqual([]);
  });

  it("reads back a slug the records tier percent-encoded", () => {
    expect(slugsOf("j-local-ada%2Fnode.rec")).toEqual(["j-local-ada/node"]);
  });

  it("refuses to read a box with no records tier as a box storing nothing", () => {
    expect(() => slugsOf("no-records-tier\n")).toThrow(
      /records\/projects\/production does not exist/,
    );
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
