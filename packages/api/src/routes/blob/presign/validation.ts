import { z } from "zod";

export const presignUploadSchema = z.object({
  projectId: z.string().min(1),
  // The logical bucket name the app declared (bucket("storage") -> "storage").
  bucket: z.string().min(1),
  files: z
    .array(
      z.object({
        // User-produced object key (the SDK computes it from the uploader's
        // path config). The tenancy prefix is prepended server-side.
        key: z.string().min(1),
        name: z.string(),
        size: z.number().int().nonnegative(),
        mimeType: z.string(),
      }),
    )
    .min(1),
  // Opaque SDK-encoded metadata bytes, base64-encoded on the wire. Stored
  // verbatim and returned unchanged on VerifyUploadSignature; never inspected.
  metadata: z.string(),
  contentDisposition: z.string().optional().default(""),
  callbackBaseUrl: z.string().min(1),
});

export type PresignUploadInput = z.infer<typeof presignUploadSchema>;
