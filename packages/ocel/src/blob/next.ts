import type { NextRequest } from "next/server";
import type { z } from "zod";
import type { Bucket } from "./bucket.js";
import {
  createRouteHandler as coreCreateRouteHandler,
  type RouteOptions,
} from "./route.js";
import { uploader as coreUploader } from "./uploader.js";
import type { ParsedInput, Uploader, UploaderAuth, UploaderUpload } from "./types.js";

export { bucket, Bucket, type BucketOptions } from "./bucket.js";
export type { RouteOptions } from "./route.js";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types.js";

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

export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): NextRouteHandlers {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return { GET, POST };
}
