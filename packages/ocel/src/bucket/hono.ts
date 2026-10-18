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

export interface HonoUploaderAuth<TInput extends StandardSchemaV1 | undefined, TMetadata> {
  input?: TInput;
  middleware: (ctx: { c: Context; input: ParsedInput<TInput> }) => MaybePromise<TMetadata>;
}

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

export type HonoRouteHandler = (c: Context) => Promise<Response>;

export function createRouteHandler(bucket: Bucket): HonoRouteHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket);
  return (c) => (c.req.method === "GET" ? GET(c.req.raw) : POST(c.req.raw, c));
}
