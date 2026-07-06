import z from "zod";
import type { Component } from "../utils/component";
import { defer } from "../utils/defer";
import { getConfig } from "../utils/get-config";
import { rpc, ResourceType } from "../utils/rpc";

export interface PostgresConfig {
  version?: string;
}

export class Postgres implements Component {
  private type = ResourceType.POSTGRES;

  constructor(
    public id: string,
    config?: PostgresConfig,
  ) {
    // intentionally defined repeatedly like this for dead code elimination when in prod
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name: id, type: this.type },
          config: { case: "postgres", value: { version: "17" } },
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

    console.log({ config });

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
