import {
  LinkType,
  type BucketProperties,
} from "../gen/proto/common/links/v1/links_pb.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { unprovisioned, unprovisionedPhase } from "../utils/phase.js";
import { rpc } from "../utils/rpc.js";
import type { AnyUploader } from "./types.js";

export interface BucketOptions<TUploaders extends Record<string, AnyUploader>> {
  allowedOrigins?: string[];
  uploaders: TUploaders;
}

export type ResolvedBucketConfig = Pick<BucketProperties, "bucket">;

export class Bucket<
  TUploaders extends Record<string, AnyUploader> = Record<string, AnyUploader>,
> {
  private type = LinkType.BUCKET;

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
    if (unprovisionedPhase()) {
      throw unprovisioned(`bucket("${this.name}")`, "__config");
    }
    return getConfig(this.name, "bucket");
  }
}

export function bucket<TUploaders extends Record<string, AnyUploader>>(
  name: string,
  options: BucketOptions<TUploaders>,
): Bucket<TUploaders> {
  return new Bucket(name, options.uploaders, options.allowedOrigins ?? []);
}
