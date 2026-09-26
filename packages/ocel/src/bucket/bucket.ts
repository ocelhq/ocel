import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import type { BucketProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { declarationSite } from "../utils/callsite.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { unprovisioned, unprovisionedPhase } from "../utils/phase.js";
import { rpc } from "../utils/rpc.js";
import { type BucketContext, resolveBucketContext } from "./bucket-context.js";
import {
  type BucketObjects,
  createObjects,
  type GetOptions,
  type ListOptions,
  type ObjectBody,
  type ObjectInfo,
  type ObjectListing,
  type PutBody,
  type PutOptions,
  type SignedUpload,
  type SignedUploadOptions,
  type SignedUrlOptions,
} from "./objects.js";
import type { AnyUploader } from "./types.js";

/** How a bucket is declared. */
export interface BucketOptions<
  TUploaders extends Record<string, AnyUploader>,
  TPublic extends boolean,
> {
  /** Serves every object anonymously over HTTP. */
  public?: TPublic;
  /** Browser origins allowed to upload straight to the store. */
  allowedOrigins?: string[];
  /** The named browser-upload flows this bucket accepts. */
  uploaders?: TUploaders;
}

/** The binding fields a bucket resolves at runtime. */
export type ResolvedBucketConfig = Pick<BucketProperties, "bucket" | "publicBaseUrl">;

/** A declared bucket, and the handle an app reads and writes its objects through. */
export class Bucket<
  TUploaders extends Record<string, AnyUploader> = Record<string, AnyUploader>,
  TPublic extends boolean = boolean,
> {
  private type = ResourceType.BUCKET;
  private context: BucketContext | undefined;
  private objects: BucketObjects;

  constructor(
    /** The name this bucket was declared under. */
    public name: string,
    /** The browser-upload flows this bucket accepts. */
    public uploaders: TUploaders,
    /** The browser origins allowed to upload straight to the store. */
    public allowedOrigins: string[],
    /** Whether every object is served anonymously over HTTP. */
    public isPublic: boolean,
  ) {
    this.objects = createObjects({ runtime: () => this.runtime() });
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: this.type },
          config: {
            case: "bucket",
            value: { allowedOrigins, public: isPublic },
          },
          source: declarationSite(),
        }),
      );
    }
  }

  private runtime(): BucketContext {
    return (this.context ??= resolveBucketContext(this));
  }

  /** The binding delivered for this bucket. */
  __config(): ResolvedBucketConfig {
    if (unprovisionedPhase()) {
      throw unprovisioned(`bucket("${this.name}")`, "__config");
    }
    return getConfig(this.name, "bucket");
  }

  /** Writes `body` under `key`, in one request or in parts, and returns the stored object. */
  put(key: string, body: PutBody, options?: PutOptions): Promise<ObjectInfo> {
    return this.objects.put(key, body, options);
  }

  /** Reads the object under `key`, or `null` when the bucket has none. */
  get(key: string, options?: GetOptions): Promise<ObjectBody | null> {
    return this.objects.get(key, options);
  }

  /** What the bucket knows about `key`, or `null` when it has none. */
  head(key: string): Promise<ObjectInfo | null> {
    return this.objects.head(key);
  }

  /** Removes one key or many; a key that is not there is not an error. */
  delete(key: string | string[]): Promise<void> {
    return this.objects.delete(key);
  }

  /** The objects under a prefix, walked as an async iterable or one page at a time. */
  list(options?: ListOptions): ObjectListing {
    return this.objects.list(options);
  }

  /** Copies `source` to `destination` within the bucket. */
  copy(source: string, destination: string): Promise<ObjectInfo> {
    return this.objects.copy(source, destination);
  }

  /** A url that reads `key` without a credential, for as long as it is valid. */
  signedUrl(key: string, options?: SignedUrlOptions): Promise<string> {
    return this.objects.signedUrl(key, options);
  }

  /** A target that writes `key` without a credential, for as long as it is valid. */
  signedUpload(key: string, options?: SignedUploadOptions): Promise<SignedUpload> {
    return this.objects.signedUpload(key, options);
  }

  /** The object's address on a bucket declared `public: true`. */
  publicUrl: TPublic extends true ? (key: string) => string : never = ((key: string) =>
    this.objects.publicUrl(key)) as TPublic extends true ? (key: string) => string : never;
}

/** Declares a bucket, and hands back the handle its objects are reached through. */
export function bucket<
  TUploaders extends Record<string, AnyUploader> = Record<string, never>,
  TPublic extends boolean = false,
>(name: string, options: BucketOptions<TUploaders, TPublic> = {}): Bucket<TUploaders, TPublic> {
  return new Bucket(
    name,
    options.uploaders ?? ({} as TUploaders),
    options.allowedOrigins ?? [],
    options.public ?? false,
  );
}
