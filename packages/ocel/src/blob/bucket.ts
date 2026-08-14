import { z } from "zod";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { rpc } from "../utils/rpc.js";
import type { AnyUploader } from "./types.js";

export interface BucketOptions<TUploaders extends Record<string, AnyUploader>> {
  allowedOrigins?: string[];
  uploaders: TUploaders;
}

const configSchema = z.object({
  address: z.string(),
  bucket: z.string(),
});

export interface ResolvedBucketConfig {
  address: string;
  bucket: string;
}

export class Bucket<
  TUploaders extends Record<string, AnyUploader> = Record<string, AnyUploader>,
> {
  private type = "ocel:bucket";

  constructor(
    public name: string,
    public uploaders: TUploaders,
    public allowedOrigins: string[],
  ) {
    if (process.env.OCEL_PHASE === "discovery") {
      const stack = new Error().stack ?? "";
      defer(
        rpc.resource.declare({
          resource: { name, type: this.type },
          config: {
            case: "bucket",
            value: { allowedOrigins },
          },
          stack,
        }),
      );
    }
  }

  __config(): ResolvedBucketConfig {
    const opts = configSchema.safeParse(
      JSON.parse(getConfig(this.name, this.type)),
    );
    if (!opts.success) {
      throw new Error(`Ocel could not resolve 'bucket(${this.name})' correctly.`);
    }
    return opts.data;
  }
}

export function bucket<TUploaders extends Record<string, AnyUploader>>(
  name: string,
  options: BucketOptions<TUploaders>,
): Bucket<TUploaders> {
  return new Bucket(name, options.uploaders, options.allowedOrigins ?? []);
}
