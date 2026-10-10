import { defineEnvVars } from "ocel/env/sveltekit";
import { z } from "zod";

export const variables = defineEnvVars({
  STRIPE_KEY: { class: "sensitive", description: "Stripe key" },
  BUILD_TAG: { class: "plain", static: true, schema: z.string().min(1) },
  PUBLIC_API_URL: { class: "plain", public: true, schema: z.string().url() },
  PUBLIC_RETRIES: {
    class: "plain",
    public: true,
    static: true,
    schema: z.string().transform(Number).pipe(z.number().int()),
  },
  PORT_HINT: {
    class: "plain",
    schema: (value: string | undefined) => (value === undefined ? 3000 : Number(value)),
  },
});
