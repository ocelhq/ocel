import type { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { declarationSite } from "./callsite.js";
import { defer } from "./defer.js";
import { unprovisionedPhase } from "./phase.js";
import { rpc } from "./rpc.js";

/**
 * Posts a reference to a resource declared elsewhere during discovery, naming the user
 * file that called it. Outside discovery it does nothing.
 */
export function reference(type: ResourceType, name: string): void {
  if (unprovisionedPhase()) {
    defer(rpc.resource.reference({ resource: { name, type }, source: declarationSite() }));
  }
}
