import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { detectWorld, expectationEnvironmentFor, type Probes } from "./world";

function probes(overrides: Partial<Probes> = {}): Probes {
  return {
    answersAsFloci: async () => true,
    callerAccount: async () => {
      throw new Error("the account was read when it should not have been");
    },
    ...overrides,
  };
}

describe("detectWorld", () => {
  it("is floci when the endpoint answers as floci", async () => {
    const asked: string[] = [];
    const where = await detectWorld(
      { AWS_ENDPOINT_URL: "http://127.0.0.1:4566" },
      probes({
        answersAsFloci: async (endpoint) => {
          asked.push(endpoint);
          return true;
        },
      }),
    );
    assert.deepEqual(where, { world: "floci", endpoint: "http://127.0.0.1:4566" });
    assert.deepEqual(asked, ["http://127.0.0.1:4566"]);
  });

  it("names the floci script when the endpoint answers as something else", async () => {
    await assert.rejects(
      detectWorld(
        { AWS_ENDPOINT_URL: "http://127.0.0.1:4566" },
        probes({ answersAsFloci: async () => false }),
      ),
      /scripts\/floci\.sh/,
    );
  });

  it("names the floci script when nothing says which aws to drive", async () => {
    await assert.rejects(detectWorld({}, probes()), /scripts\/floci\.sh/);
  });

  it("is real when the credentials resolve to the expected account", async () => {
    const where = await detectWorld(
      { EXPECTED_AWS_ACCOUNT_ID: "111122223333" },
      probes({ callerAccount: async () => "111122223333" }),
    );
    assert.deepEqual(where, { world: "real" });
  });

  it("refuses an account the run may not touch", async () => {
    await assert.rejects(
      detectWorld(
        { EXPECTED_AWS_ACCOUNT_ID: "111122223333" },
        probes({ callerAccount: async () => "999988887777" }),
      ),
      /999988887777.*111122223333/s,
    );
  });

  it("prefers the endpoint over an account the environment also carries", async () => {
    const where = await detectWorld(
      { AWS_ENDPOINT_URL: "http://127.0.0.1:4566", EXPECTED_AWS_ACCOUNT_ID: "111122223333" },
      probes(),
    );
    assert.equal(where.world, "floci");
  });
});

describe("expectationEnvironmentFor", () => {
  it("chooses the file each world owns", () => {
    assert.equal(expectationEnvironmentFor("floci"), "aws.floci");
    assert.equal(expectationEnvironmentFor("real"), "aws");
  });
});
