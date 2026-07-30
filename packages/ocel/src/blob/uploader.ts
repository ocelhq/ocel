import type { z } from "zod";
import type {
  BlobRequest,
  ParsedInput,
  Uploader,
  UploaderAuth,
  UploaderUpload,
} from "./types.js";

/**
 * Declares a named uploader. `auth` runs first at presign (validate `input`,
 * then `middleware` produces the trusted metadata); `upload` shapes storage
 * (accept/limits/path/contentDisposition) and runs onUploadComplete when the
 * upload lands. Metadata inferred from middleware threads into every `upload`
 * hook, so mismatches are compile errors.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
  TReq = BlobRequest,
>(
  auth: UploaderAuth<TReq, TInput, TMetadata>,
  upload: UploaderUpload<TMetadata> = {},
): Uploader<ParsedInput<TInput>, TMetadata, TReq> {
  return {
    auth: auth as UploaderAuth<TReq, z.ZodType | undefined, TMetadata>,
    upload,
  };
}
