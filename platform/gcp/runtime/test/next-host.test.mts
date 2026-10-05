import { expect, test } from "vitest";
import { newGcpNextHost } from "../src/next/next-host.mjs";

test("the GCP host binds every network on the port Cloud Run names", () => {
  expect(newGcpNextHost({ PORT: "8080" }).bind).toEqual({ host: "0.0.0.0", port: 8080 });
});

test("the GCP host refuses to start where nothing names a port", () => {
  expect(() => newGcpNextHost({})).toThrow(/PORT/);
});
