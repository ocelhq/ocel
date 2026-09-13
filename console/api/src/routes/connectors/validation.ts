import { COMPUTE_KINDS, CONNECTOR_REACHES, CONNECTOR_VENDORS } from "@console/db/schema";
import { z } from "zod";

const targetSchema = z
  .string()
  .min(1)
  .max(256)
  .regex(/^[^/\s]+(?:\/[^/\s]+)+$/, "A target names its vendor and the account it fingerprints");

export const upsertConnectorSchema = z.object({
  target: targetSchema,
  vendor: z.enum(CONNECTOR_VENDORS),
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
