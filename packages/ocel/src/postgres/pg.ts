import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import type { PostgresProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { declarationSite } from "../utils/callsite.js";
import type { Component } from "../utils/component.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { rpc } from "../utils/rpc.js";

export interface PostgresConfig {
  version?: string;
}

export class Postgres implements Component {
  private type = ResourceType.POSTGRES;

  constructor(
    public id: string,
    config?: PostgresConfig,
  ) {
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name: id, type: this.type },
          config: {
            case: "postgres",
            value: { version: config?.version ?? "17" },
          },
          source: declarationSite(),
        }),
      );
    }
  }

  __id(): string {
    return this.id;
  }

  __config(): PostgresProperties {
    return getConfig(this.id, "postgres");
  }
}
