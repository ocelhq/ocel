import { z } from "zod";
import { defer } from "../utils/defer";
import { getConfig } from "../utils/get-config";
import { rpc, ResourceType } from "../utils/rpc";
import type { AnyUploader } from "./types";

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
  private type = ResourceType.BUCKET;

  constructor(
    public name: string,
    public uploaders: TUploaders,
    public allowedOrigins: string[],
  ) {
    // intentionally defined repeatedly like this for dead code elimination when in prod
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: this.type },
          config: {
            case: "bucket",
            value: { allowedOrigins },
          },
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
