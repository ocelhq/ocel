import { describe, expect, it } from "bun:test";
import { plannedWrites } from "./planStream";

describe("plannedWrites", () => {
  it("reads a plan that keeps and adopts as writing nothing", () => {
    const kept = [
      "INFO  [plan] a line a human reads",
      '{"operation":{"time":"2026-09-27T10:00:01Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","started":{}}}',
      '{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP","changes":[{"name":"OcelDispatchFunction","action":"ACTION_KEEP"},{"name":"OcelOriginSecret","action":"ACTION_ADOPT"}]},{"kind":"edge","name":"cloudfront/edge","action":"ACTION_ADOPT"}]}}}',
    ].join("\n");
    expect(plannedWrites(kept)).toEqual([]);
  });

  it("names the change a group writes rather than the group, and a group that writes with no change of its own", () => {
    const mixed =
      '{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_UPDATE","changes":[{"name":"OcelDispatchFunction","action":"ACTION_UPDATE"},{"name":"OcelOriginSecret","action":"ACTION_KEEP"}]},{"kind":"edge","name":"cloudflare/edge","action":"ACTION_CREATE"}]}}}';
    expect(plannedWrites(mixed)).toEqual([
      "aws/ocel-bootstrap/OcelDispatchFunction ACTION_UPDATE",
      "cloudflare/edge ACTION_CREATE",
    ]);
  });

  it("refuses a stream with no plan phase rather than reading it as writing nothing", () => {
    expect(() => plannedWrites("Bootstrapped production\n")).toThrow(/no plan phase/);
  });
});
