import { afterEach, describe, expect, it } from "vitest";
import { deployment } from "./deployment.js";
import { EnvValueError } from "./errors.js";

const held = process.env.OCEL_URL;

afterEach(() => {
  if (held === undefined) delete process.env.OCEL_URL;
  else process.env.OCEL_URL = held;
});

describe("deployment.url", () => {
  it("hands back the url ocel delivered, scheme and all", () => {
    process.env.OCEL_URL = "https://web-j-1.ocel.site";

    expect(deployment.url).toBe("https://web-j-1.ocel.site");
  });

  it("follows the value, so a re-resolve under ocel dev is picked up", () => {
    process.env.OCEL_URL = "http://localhost:3000";
    expect(deployment.url).toBe("http://localhost:3000");

    process.env.OCEL_URL = "http://localhost:4321";
    expect(deployment.url).toBe("http://localhost:4321");
  });

  it("throws where nothing delivered one, naming what would deliver it", () => {
    delete process.env.OCEL_URL;

    expect(() => deployment.url).toThrow(EnvValueError);
    expect(() => deployment.url).toThrow(/domains.production/);
  });
});
