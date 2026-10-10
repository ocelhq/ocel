import { defineEnvVars } from "ocel/env/sveltekit";
import { defaultApi } from "$lib/urls";

export const variables = defineEnvVars({
  API: { class: "plain", schema: (value: string | undefined) => value ?? defaultApi },
});
