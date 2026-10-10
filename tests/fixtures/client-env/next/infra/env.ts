import { defineEnv } from "ocel/env/next";
import { z } from "zod";

export const env = defineEnv({
  NEXT_PUBLIC_GREETING: { class: "plain", schema: z.string().startsWith("journey-") },
  SENSITIVE_TOKEN: { class: "sensitive" },
});
