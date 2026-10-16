import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { NonzeroExitError, type Ran } from "../../ocel";
import { BOOTSTRAP_REFRESH_ARGS, bootstrapWithVariablesKey, cellBootstrapArgs } from "./bootstrap";

const ran = (stdout = ""): Ran => ({ code: 0, stdout, stderr: "" });

function refused(args: string[], stdout: string): NonzeroExitError {
  return new NonzeroExitError(args, { code: 1, stdout, stderr: "" });
}

describe("cellBootstrapArgs", () => {
  it("bootstraps every AWS edge with no flag naming a part, the edge's own coming from the config", () => {
    for (const edge of [undefined, "cloudfront", "api-gateway", "cloudflare"] as const) {
      assert.deepEqual(cellBootstrapArgs({ config: { edge } }), BOOTSTRAP_REFRESH_ARGS);
    }
  });

  it("refuses an edge no AWS bootstrap fronts", () => {
    assert.throws(() => cellBootstrapArgs({ config: { edge: "alb" } }), /alb edge/);
  });
});

describe("bootstrapWithVariablesKey", () => {
  it("bootstraps again once a write refused for want of the key has recorded that the project wants it", async () => {
    const said: string[] = [];
    await bootstrapWithVariablesKey(
      async () => {
        said.push("bootstrap");
        return ran();
      },
      async (args) => {
        said.push(args.join(" "));
        throw refused(
          args,
          "this account's Ocel bootstrap lacks a part this project wants: variables-key",
        );
      },
    );
    assert.deepEqual(said, ["bootstrap", "env set E2E_VARIABLES_KEY_WANTED=wanted", "bootstrap"]);
  });

  it("removes the value it wrote where the key was already there", async () => {
    const said: string[] = [];
    await bootstrapWithVariablesKey(
      async () => {
        said.push("bootstrap");
        return ran();
      },
      async (args) => {
        said.push(args.join(" "));
        return ran();
      },
    );
    assert.deepEqual(said, [
      "bootstrap",
      "env set E2E_VARIABLES_KEY_WANTED=wanted",
      "env rm E2E_VARIABLES_KEY_WANTED",
    ]);
  });

  it("passes on a refusal that is not for the key", async () => {
    await assert.rejects(
      bootstrapWithVariablesKey(
        async () => ran(),
        async (args) => {
          throw refused(args, "credential check failed");
        },
      ),
      /credential check failed/,
    );
  });
});
