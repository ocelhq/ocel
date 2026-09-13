import type { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { declarationSite } from "./callsite.js";
import { defer } from "./defer.js";
import { unprovisionedPhase } from "./phase.js";
import { rpc } from "./rpc.js";

export function reference(type: ResourceType, name: string): void {
  if (unprovisionedPhase()) {
    defer(rpc.resource.reference({ resource: { name, type }, source: declarationSite() }));
  }
}
