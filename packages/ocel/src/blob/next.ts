import type { NextRequest } from "next/server";
import type { z } from "zod";
import type { Bucket } from "./bucket";
import {
  createRouteHandler as coreCreateRouteHandler,
  type RouteOptions,
} from "./route";
import { uploader as coreUploader } from "./uploader";
import type { ParsedInput, Uploader, UploaderAuth, UploaderUpload } from "./types";

export { bucket, Bucket, type BucketOptions } from "./bucket";
export type { RouteOptions } from "./route";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types";

/**
 * The Next binding of `uploader`. Identical to the framework-agnostic core
 * except that `middleware`'s `req` is typed as a Next `NextRequest`. The core
 * remains framework-agnostic; this only narrows the request type.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<NextRequest, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, NextRequest> {
  return coreUploader<TInput, TMetadata, NextRequest>(auth, upload);
}

export interface NextRouteHandlers {
  GET: (req: NextRequest) => Promise<Response>;
  POST: (req: NextRequest) => Promise<Response>;
}

/**
 * The Next binding of `createRouteHandler`. Returns App Router `{ GET, POST }`
 * handlers typed against `NextRequest` — export them straight from an
 * `app/.../route.ts`. The mapping is the identity: a `NextRequest` already
 * satisfies the core, so this only narrows the handler types.
 */
export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): NextRouteHandlers {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return { GET, POST };
}
