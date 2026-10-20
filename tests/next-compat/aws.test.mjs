import { describe, expect, it } from "vitest";

import { previewBootstrapStack } from "./aws.mjs";

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
