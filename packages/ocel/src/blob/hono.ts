import type { Context } from "hono";
import type { z } from "zod";
import type { Bucket } from "./bucket.js";
import {
  createRouteHandler as coreCreateRouteHandler,
  type RouteOptions,
} from "./route.js";
import { uploader as coreUploader } from "./uploader.js";
import type {
  MaybePromise,
  ParsedInput,
  Uploader,
  UploaderUpload,
} from "./types.js";

export { bucket, Bucket, type BucketOptions } from "./bucket.js";
export type { RouteOptions } from "./route.js";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types.js";

export interface HonoUploaderAuth<
  TInput extends z.ZodType | undefined,
  TMetadata,
> {
  input?: TInput;
  middleware: (ctx: {
    c: Context;
    input: ParsedInput<TInput>;
  }) => MaybePromise<TMetadata>;
}

export function uploader<
  TInput extends z.ZodType | undefined = undefined,
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

export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): HonoRouteHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return (c) => (c.req.method === "GET" ? GET(c.req.raw) : POST(c.req.raw, c));
}
