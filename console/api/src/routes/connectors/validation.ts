import { COMPUTE_KINDS, CONNECTOR_REACHES } from "@console/db/schema";
import { z } from "zod";

const SEGMENT = "[a-z0-9._:-]+";

const vendorSchema = z
  .string()
  .max(64)
  .regex(
    new RegExp(`^${SEGMENT}$`),
    "A vendor carries lower-case letters, digits, dot, underscore, colon and dash",
  );

const targetSchema = z
  .string()
  .max(256)
  .regex(
    new RegExp(`^${SEGMENT}(?:/${SEGMENT})+$`),
    "A target names its vendor and the account it fingerprints",
  );

export const upsertConnectorSchema = z.object({
  target: targetSchema,
  vendor: vendorSchema,
  reach: z.enum(CONNECTOR_REACHES).default("dial"),
});

export const patchConnectorSchema = z
  .object({
    url: z.url().max(2048).nullable().optional(),
    publicKey: z.string().min(1).max(256).nullable().optional(),
    tlsPin: z.string().min(1).max(256).nullable().optional(),
    compute: z.enum(COMPUTE_KINDS).optional(),
  })
  .refine(
    (body) => Object.keys(body).length > 0,
    "Name at least one of url, publicKey, tlsPin or compute",
  );

export const heartbeatSchema = z.object({
  version: z.string().min(1).max(64),
  capabilities: z.array(z.string().min(1).max(128)).max(32),
});

export type UpsertConnectorInput = z.infer<typeof upsertConnectorSchema>;
