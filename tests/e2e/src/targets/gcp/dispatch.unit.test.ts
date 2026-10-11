import { describe, expect, it } from "bun:test";
import { dispatchArgs } from "./dispatch";

describe("dispatchArgs", () => {
  it("passes the dispatcher its emulator, project and region", () => {
    const args = dispatchArgs("http://127.0.0.1:4588", "floci-local", "europe-west1");

    expect(args).toEqual([
      "-endpoint",
      "http://127.0.0.1:4588",
      "-project",
      "floci-local",
      "-region",
      "europe-west1",
    ]);
  });
});
