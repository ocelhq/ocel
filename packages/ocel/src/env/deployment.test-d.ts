import { describe, expectTypeOf, it } from "vitest";
import { deployment } from "./index.js";
import { deployment as edgeDeployment } from "./edge.js";

describe("the deployment ocel writes", () => {
  it("types its url as a string an app may read without a fallback", () => {
    expectTypeOf(deployment.url).toEqualTypeOf<string>();
    expectTypeOf(edgeDeployment.url).toEqualTypeOf<string>();
  });

  it("is read-only", () => {
    expectTypeOf(deployment).toExtend<{ readonly url: string }>();
    // @ts-expect-error ocel writes the url; an app never does
    deployment.url = "https://example.com";
  });
});
