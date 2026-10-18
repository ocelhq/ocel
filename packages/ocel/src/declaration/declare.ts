import type { MessageInitShape } from "@bufbuild/protobuf";
import { isDiscovering } from "../binding/unprovisioned.js";
import {
  type DeclareRequestSchema,
  ResourceType,
} from "../gen/proto/app/resources/v1/resources_pb.js";
import { OCEL_DEV_SERVER } from "../runtime/dev-server.js";
import { rpc } from "../runtime/rpc.js";
import { declarationSite } from "./callsite.js";

declare global {
  var __ocelRegister: Promise<unknown>[];
}

export type ResourceConfig = Exclude<
  MessageInitShape<typeof DeclareRequestSchema>["config"],
  { case: undefined } | undefined
>;

export function holdRegistration(registration: Promise<unknown>): void {
  if (!OCEL_DEV_SERVER) {
    throw new Error("OCEL_DEV_SERVER environment variable is not set");
  }
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(registration);
}

export function declare(name: string, config: ResourceConfig, source?: string): void {
  if (!isDiscovering()) return;
  const type = ResourceType[config.case.toUpperCase() as Uppercase<ResourceConfig["case"]>];
  holdRegistration(
    rpc.resource.declare({
      resource: { name, type },
      config,
      source: source ?? declarationSite(),
    }),
  );
}
