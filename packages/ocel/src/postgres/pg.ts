import {
  LinkType,
  type PostgresProperties,
} from "../gen/proto/common/links/v1/links_pb.js";
import type { Component } from "../utils/component.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { rpc } from "../utils/rpc.js";

export interface PostgresConfig {
  version?: string;
}

export class Postgres implements Component {
  private type = LinkType.POSTGRES;

  constructor(
    public id: string,
    config?: PostgresConfig,
  ) {
    if (process.env.OCEL_PHASE === "discovery") {
      const stack = new Error().stack ?? "";
      defer(
        rpc.resource.declare({
          resource: { name: id, type: this.type },
          config: {
            case: "postgres",
            value: { version: config?.version ?? "17" },
          },
          stack,
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
