import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { Context } from "hono";
import type { Bucket } from "./bucket.js";
import { createRouteHandler as coreCreateRouteHandler } from "./route.js";
import type { MaybePromise, ParsedInput, Uploader, UploaderUpload } from "./types.js";
import { uploader as coreUploader } from "./uploader.js";

export { Bucket, type BucketOptions, bucket } from "./bucket.js";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types.js";

/** Who may upload through a flow, decided from the Hono context. */
export interface HonoUploaderAuth<TInput extends StandardSchemaV1 | undefined, TMetadata> {
  /** A Standard Schema the client's input is validated against. */
  input?: TInput;
  /** Returns the metadata the upload carries, or throws to refuse the upload. */
  middleware: (ctx: { c: Context; input: ParsedInput<TInput> }) => MaybePromise<TMetadata>;
}

/**
 * Declares a browser-upload flow: `auth` decides from the Hono context who may upload and
 * returns the metadata the upload carries, and `upload` bounds what is accepted and where
 * it is stored.
 */
export function uploader<
  TInput extends StandardSchemaV1 | undefined = undefined,
  TMetadata = unknown,
>(
  auth: HonoUploaderAuth<TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, Context> {
  return coreUploader<TInput, TMetadata, Context>(
    {
      input: auth.input,
      middleware: ({ req, input }) => auth.middleware({ c: req, input }),
    },
    upload,
  );
}

/** A Hono handler, mounted for both GET and POST on one path. */
export type HonoRouteHandler = (c: Context) => Promise<Response>;

/** The Hono handler that serves `bucket`'s uploaders to the browser upload client. */
export function createRouteHandler(bucket: Bucket): HonoRouteHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket);
  return (c) => (c.req.method === "GET" ? GET(c.req.raw) : POST(c.req.raw, c));
}
