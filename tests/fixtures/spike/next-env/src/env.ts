import { defineEnv } from "ocel/env/next";
import { z } from "zod";

export const env = defineEnv({
  STRIPE_KEY: { class: "sensitive" },
  NEXT_PUBLIC_API_URL: { class: "plain", schema: z.string().url() },
  NEXT_PUBLIC_RETRIES: { class: "plain", schema: z.coerce.number().int() },
});
