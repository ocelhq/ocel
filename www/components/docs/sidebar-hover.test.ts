import { describe, expect, it } from "vitest";
import { follow } from "./sidebar-hover";

describe("follow", () => {
  it("places the marker on a hovered item still in the sidebar", () => {
    expect(follow({ isConnected: true }, true)).toBe("place");
  });

  it("hides the marker once its hovered item has left the sidebar", () => {
    expect(follow({ isConnected: false }, true)).toBe("hide");
  });

  it("leaves a hidden marker alone", () => {
    expect(follow({ isConnected: true }, false)).toBe("stay");
    expect(follow(null, false)).toBe("stay");
  });
});
