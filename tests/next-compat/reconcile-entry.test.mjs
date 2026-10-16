import { describe, expect, it } from "vitest";

import { bootstrapWithVariablesKey } from "./reconcile-entry.mjs";

function recording(answers) {
  const ran = [];
  const run = (_adapterDir, _dir, args) => {
    ran.push(args.join(" "));
    return answers(args);
  };
  return { ran, run };
}

describe("bootstrapWithVariablesKey", () => {
  it("bootstraps again once a write refused for want of the key has recorded that the entry wants it", () => {
    const { ran, run } = recording((args) =>
      args[0] === "env"
        ? { failure: "exited with 1", said: "lacks a part this project wants: variables-key" }
        : { failure: "", said: "" },
    );

    expect(bootstrapWithVariablesKey("adapter", "dir", run)).toBe("");
    expect(ran).toEqual([
      "bootstrap preview --yes",
      "env set E2E_VARIABLES_KEY_WANTED=wanted --preview",
      "bootstrap preview --yes",
    ]);
  });

  it("removes the value it wrote where the key was already there", () => {
    const { ran, run } = recording(() => ({ failure: "", said: "" }));

    expect(bootstrapWithVariablesKey("adapter", "dir", run)).toBe("");
    expect(ran).toEqual([
      "bootstrap preview --yes",
      "env set E2E_VARIABLES_KEY_WANTED=wanted --preview",
      "env rm E2E_VARIABLES_KEY_WANTED --preview",
    ]);
  });

  it("passes on a refusal that is not for the key", () => {
    const { run } = recording((args) =>
      args[0] === "env"
        ? { failure: "exited with 1", said: "credential check failed" }
        : { failure: "", said: "" },
    );

    expect(bootstrapWithVariablesKey("adapter", "dir", run)).toBe("exited with 1");
  });
});
