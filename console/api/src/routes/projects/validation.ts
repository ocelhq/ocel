import { FRAMEWORKS } from "@console/db/schema";
import { z } from "zod";

const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

const frameworksSchema = z
  .array(z.enum(FRAMEWORKS))
  .max(FRAMEWORKS.length)
  .transform((values) => [...new Set(values)]);

export const createProjectSchema = z.object({
  name: z.string().min(1).max(100),
  slug: z.string().min(1).max(63).regex(SLUG_PATTERN),
  description: z.string().nullable().optional(),
  frameworks: frameworksSchema.optional(),
});

export const updateProjectSchema = z.object({
  frameworks: frameworksSchema,
});

export type CreateProjectInput = z.infer<typeof createProjectSchema>;
