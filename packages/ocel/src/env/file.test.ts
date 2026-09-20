import { afterEach, describe, expect, it } from "vitest";
import { readLiveFile } from "./file.js";

const node = globalThis.process;

function runningOn(host: unknown): void {
  (globalThis as { process?: unknown }).process = host;
}

afterEach(() => {
  runningOn(node);
});

describe("reading a binding out of the projected live directory", () => {
  it("is skipped on a runtime that has no files to read", () => {
    runningOn({ env: { OCEL_LIVE_DIR: "/live" } });

    expect(readLiveFile("OCEL_RESOURCE_BUCKET_storage")).toBeUndefined();
  });

  it("names the node it runs on when that node cannot hand out its own fs", () => {
    runningOn({ env: { OCEL_LIVE_DIR: "/live" }, versions: { node: "20.11.0" } });

    expect(() => readLiveFile("OCEL_RESOURCE_BUCKET_storage")).toThrow(/node 22\.3/);
    expect(() => readLiveFile("OCEL_RESOURCE_BUCKET_storage")).toThrow(/20\.11\.0/);
  });
});
