import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import type { BucketProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { declarationSite } from "../utils/callsite.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { unprovisioned, unprovisionedPhase } from "../utils/phase.js";
import { reference } from "../utils/reference.js";
import { rpc } from "../utils/rpc.js";
import type { AnyUploader } from "./types.js";

export interface BucketOptions<TUploaders extends Record<string, AnyUploader>> {
  allowedOrigins?: string[];
  uploaders: TUploaders;
}

/** The options of {@link bucket.ref}: the declaration owns the bucket's own configuration. */
export type BucketRefOptions<TUploaders extends Record<string, AnyUploader>> = Pick<
  BucketOptions<TUploaders>,
  "uploaders"
>;

export type ResolvedBucketConfig = Pick<BucketProperties, "bucket">;

export class Bucket<TUploaders extends Record<string, AnyUploader> = Record<string, AnyUploader>> {
  constructor(
    public name: string,
    public uploaders: TUploaders,
  ) {}

  __config(): ResolvedBucketConfig {
    if (unprovisionedPhase()) {
      throw unprovisioned(`bucket("${this.name}")`, "__config");
    }
    return getConfig(this.name, "bucket");
  }
}

/**
 * Declares a bucket named `name` and returns the handle its uploaders are served through.
 * Call it from a file under the project's discovery folder: during discovery the call is
 * the declaration, and at runtime it reads the binding the deploy delivered for that name.
 */
export function bucket<TUploaders extends Record<string, AnyUploader>>(
  name: string,
  options: BucketOptions<TUploaders>,
): Bucket<TUploaders> {
  if (unprovisionedPhase()) {
    defer(
      rpc.resource.declare({
        resource: { name, type: ResourceType.BUCKET },
        config: { case: "bucket", value: { allowedOrigins: options.allowedOrigins ?? [] } },
        source: declarationSite(),
      }),
    );
  }
  return new Bucket(name, options.uploaders);
}

/**
 * References the bucket named `name`, declared once elsewhere in the project in any
 * language, and returns the same handle {@link bucket} does. It never declares. Call it
 * from a file under the project's discovery folder and import that file from the app:
 * during discovery the call records that the file uses the bucket, so the deploy grants it
 * to every app that imports the file.
 */
bucket.ref = <TUploaders extends Record<string, AnyUploader>>(
  name: string,
  options: BucketRefOptions<TUploaders>,
): Bucket<TUploaders> => {
  reference(ResourceType.BUCKET, name);
  return new Bucket(name, options.uploaders);
};
