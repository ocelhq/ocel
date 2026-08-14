import z from "zod";
import type { Component } from "../utils/component.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { rpc } from "../utils/rpc.js";

export interface PostgresConfig {
  version?: string;
}

export class Postgres implements Component {
  private type = "ocel:postgres";

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

  __config() {
    const config = JSON.parse(getConfig(this.id, this.type));
    const schema = z.object({
      connectionString: z.string(),
    });

    const opts = schema.safeParse(config);
    if (!opts.success) {
      throw new Error(
        `Ocel could not resolve 'postgres(${this.id})' correctly.`,
      );
    }

    return {
      connectionString: opts.data.connectionString,
    };
  }
}
