import type { NextRequest } from "next/server";
import type { z } from "zod";
import { uploader as coreUploader } from "./uploader";
import type { ParsedInput, Uploader, UploaderAuth, UploaderUpload } from "./types";

export { bucket, Bucket, type BucketOptions } from "./bucket";
export { createRouteHandler, type RouteOptions } from "./route";
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
