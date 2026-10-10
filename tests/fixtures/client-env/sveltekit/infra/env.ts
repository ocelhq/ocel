import { defineEnvVars } from "ocel/env/sveltekit";
import { z } from "zod";

export const variables = defineEnvVars({
  PUBLIC_GREETING: { class: "plain", public: true, schema: z.string().startsWith("journey-") },
  SENSITIVE_TOKEN: { class: "sensitive" },
});
