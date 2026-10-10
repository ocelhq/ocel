import { defineEnvVars } from "ocel/env/sveltekit";
import { dev } from "$app/environment";

export const variables = defineEnvVars({
  API: { class: "plain", schema: (value: string | undefined) => value ?? (dev ? "dev" : "prod") },
});
