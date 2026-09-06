import { z } from "zod";

const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

export const envKeySchema = z.string().min(1).max(128).regex(KEY_PATTERN);

export const putEnvValueSchema = z.object({
  value: z.string().max(64 * 1024),
});
