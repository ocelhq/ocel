import type { z } from "zod";
import type {
  ParsedInput,
  Uploader,
  UploaderAuth,
  UploaderUpload,
  UploadRequest,
} from "./types.js";

export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
  TReq = UploadRequest,
>(
  auth: UploaderAuth<TReq, TInput, TMetadata>,
  upload: UploaderUpload<TMetadata> = {},
): Uploader<ParsedInput<TInput>, TMetadata, TReq> {
  return {
    auth: auth as UploaderAuth<TReq, z.ZodType | undefined, TMetadata>,
    upload,
  };
}
