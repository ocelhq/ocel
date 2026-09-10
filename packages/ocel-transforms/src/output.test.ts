import { describe, expect, it } from "vitest";
import { bindings, isBindingOutput } from "./output";

describe("bindings", () => {
  it("names the resource type, the binding and the property a deploy resolves", () => {
    expect(bindings.postgres!.orders!.host).toEqual({
      $ocelOutput: { type: "postgres", name: "orders", property: "host" },
    });
  });

  it("reads a record nothing declared under custom", () => {
    expect(bindings.custom!.network!.subnetIds).toEqual({
      $ocelOutput: { type: "custom", name: "network", property: "subnetIds" },
    });
  });

  it("stays undefined for the keys a thenable would be asked for", () => {
    expect((bindings as Record<string, unknown>).then).toBeUndefined();
    expect((bindings.custom as Record<string, unknown>).then).toBeUndefined();
    expect((bindings.custom!.network as Record<string, unknown>).then).toBeUndefined();
  });

  it("refuses a placeholder that names no property", () => {
    expect(() => bindings.custom!.network![""]).toThrow(/names no property/);
  });

  it("refuses a placeholder that names no binding", () => {
    expect(() => bindings.custom![""]!.subnetIds).toThrow(/names no binding/);
  });

  it("recognises a placeholder wherever one is written", () => {
    expect(isBindingOutput(bindings.custom!.network!.subnetIds)).toBe(true);
    expect(isBindingOutput({ subnetIds: [] })).toBe(false);
  });
});
