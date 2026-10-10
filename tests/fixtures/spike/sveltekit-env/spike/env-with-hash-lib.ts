import { defineEnvVars } from "ocel/env/sveltekit";
import { defaultApi } from "#lib/urls.ts";

export const variables = defineEnvVars({
  API: { class: "plain", schema: (value: string | undefined) => value ?? defaultApi },
});
