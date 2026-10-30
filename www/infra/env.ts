import { defineEnv } from "ocel/env/next";

export const env = defineEnv({
  NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN: { class: "plain" },
  NEXT_PUBLIC_POSTHOG_HOST: { class: "plain" },
});
