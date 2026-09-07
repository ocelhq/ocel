import { describe, expect, it } from "vitest";

import { previewBootstrapStack } from "./aws.mjs";

describe("previewBootstrapStack", () => {
  it("names the preview bootstrap of the default namespace when nothing names one", () => {
    expect(previewBootstrapStack({})).toBe("ocel-bootstrap-preview");
    expect(previewBootstrapStack({ OCEL_NAMESPACE: "" })).toBe("ocel-bootstrap-preview");
  });

  it("names the preview bootstrap of the namespace this run drives", () => {
    expect(previewBootstrapStack({ OCEL_NAMESPACE: "j-1874-next" })).toBe(
      "j-1874-next-bootstrap-preview",
    );
  });
});
