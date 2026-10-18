import { lazyBinding } from "../binding/binding.js";
import { declare } from "../declaration/declare.js";
import { type BucketContext, openBucketContext } from "./bucket-context.js";
import {
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

const contexts = new WeakMap<Bucket, (access: string) => BucketContext>();

export function bucketContextOf(bucket: Bucket, access: string): BucketContext {
  const context = contexts.get(bucket);
  if (!context) {
    throw new Error(`bucket("${bucket.name}") was not declared with bucket()`);
  }
  return context(access);
}

/** A declared bucket, and the handle an app reads and writes its objects through. */
export class Bucket<
  TUploaders extends Record<string, AnyUploader> = Record<string, AnyUploader>,
  TPublic extends boolean = boolean,
> {
  constructor(
    /** The name this bucket was declared under. */
    readonly name: string,
    /** The browser-upload flows this bucket accepts. */
    readonly uploaders: TUploaders,
    /** The browser origins allowed to upload straight to the store. */
    readonly allowedOrigins: string[],
    /** Whether every object is served anonymously over HTTP. */
    readonly isPublic: boolean,
  ) {
    const context = lazyBinding(`bucket("${name}")`, name, "bucket", openBucketContext);
    contexts.set(this, context);
    const objects = createObjects({ runtime: context });
    this.put = objects.put;
    this.get = objects.get;
    this.head = objects.head;
    this.delete = objects.delete;
    this.list = objects.list;
    this.copy = objects.copy;
    this.signedUrl = objects.signedUrl;
    this.signedUpload = objects.signedUpload;
    this.publicUrl = objects.publicUrl as typeof this.publicUrl;
    declare(name, { case: "bucket", value: { allowedOrigins, public: isPublic } });
  }

  /** Writes `body` under `key`, in one request or in parts, and returns the info of the version this write stored. */
  readonly put: (key: string, body: PutBody, options?: PutOptions) => Promise<ObjectInfo>;

  /**
   * Reads the object under `key`, or `undefined` when the bucket has none. Throws
   * `PreconditionFailedError` when the object keeps changing while it is read.
   */
  readonly get: (key: string, options?: GetOptions) => Promise<ObjectBody | undefined>;

  /** What the bucket knows about `key`, or `undefined` when it has none. */
  readonly head: (key: string) => Promise<ObjectInfo | undefined>;

  /** Removes one key or many; a key that is not there is not an error. */
  readonly delete: (key: string | string[]) => Promise<void>;

  /** The objects under a prefix, walked as an async iterable or one page at a time. */
  readonly list: (options?: ListOptions) => ObjectListing;

  /** Copies `source` to `destination` within the bucket. */
  readonly copy: (source: string, destination: string) => Promise<ObjectInfo>;

  /** A url that reads `key` without a credential, for as long as it is valid. */
  readonly signedUrl: (key: string, options?: SignedUrlOptions) => Promise<string>;

  /** A target that writes `key` without a credential, for as long as it is valid. */
  readonly signedUpload: (key: string, options?: SignedUploadOptions) => Promise<SignedUpload>;

  /** The object's address on a bucket declared `public: true`. */
  readonly publicUrl: TPublic extends true ? (key: string) => string : never;
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
