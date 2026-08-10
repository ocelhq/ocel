import { z } from "zod";

export const presignUploadSchema = z.object({
  projectId: z.string().min(1),
  bucket: z.string().min(1),
  files: z
    .array(
      z.object({
        key: z.string().min(1),
        name: z.string(),
        size: z.number().int().nonnegative(),
        mimeType: z.string(),
      }),
    )
    .min(1),
  metadata: z.string(),
  contentDisposition: z.string().optional().default(""),
  callbackBaseUrl: z.string().min(1),
});

export type PresignUploadInput = z.infer<typeof presignUploadSchema>;
