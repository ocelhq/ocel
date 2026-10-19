import { describe, expect, it } from "vitest";

import { previewBootstrapped, previewBootstrapStack } from "./aws.mjs";

describe("previewBootstrapStack", () => {
  it("names the preview bootstrap of the default namespace when nothing names one", () => {
    expect(previewBootstrapStack({})).toBe("ocel-preview-bootstrap");
    expect(previewBootstrapStack({ OCEL_NAMESPACE: "" })).toBe("ocel-preview-bootstrap");
  });

  it("names the preview bootstrap of the namespace this run drives", () => {
    expect(previewBootstrapStack({ OCEL_NAMESPACE: "j-1874-next" })).toBe(
      "j-1874-next-preview-bootstrap",
    );
    expect(previewBootstrapStack({ OCEL_NAMESPACE: "j-1874-deploy-node" })).toBe(
      "j-187-e5b227-preview-bootstrap",
    );
  });
});

describe("previewBootstrapped", () => {
  const env = { OCEL_NAMESPACE: "e2e-next-compat" };

  it("reads the preview bootstrap stack of the namespace this run drives", () => {
    const asked = [];
    expect(
      previewBootstrapped((args) => {
        asked.push(args);
        return "CREATE_COMPLETE";
      }, env),
    ).toBe(true);
    expect(asked[0]).toContain(previewBootstrapStack(env));
  });

  it("answers that a namespace with no preview bootstrap stack is not bootstrapped", () => {
    const missing = () => {
      throw Object.assign(new Error("Command failed"), {
        stderr: `Stack with id ${previewBootstrapStack(env)} does not exist`,
      });
    };
    expect(previewBootstrapped(missing, env)).toBe(false);
  });

  it("passes on a failure to read the stack rather than answering that nothing is bootstrapped", () => {
    const denied = () => {
      throw Object.assign(new Error("Command failed"), { stderr: "AccessDenied" });
    };
    expect(() => previewBootstrapped(denied, env)).toThrow(/Command failed/);
  });
});
