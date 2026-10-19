import { describe, expect, it } from "bun:test";
import { svelteKitChecks } from "./sveltekit";

describe("the sveltekit checks", () => {
  it("hold the cross-origin form refusal to a deployment, since vite dev checks no origin", () => {
    const refusal = svelteKitChecks.find(
      (one) => one.title === "a form action posted from another origin is refused",
    );
    expect(refusal?.assertsDeployment).toBe(true);
  });
});
