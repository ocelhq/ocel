import { defineEnv } from "ocel/env";

export const env = defineEnv({
  STRIPE_API_KEY: {
    class: "sensitive",
  },
  POSTHOG_PROJECT_ID: {
    class: "plain",
  },
  NEXT_PUBLIC_APP_ID: {
    class: "plain",
    client: true,
  },
  SOME_FOLDER_VALUE: {
    class: "sensitive",
    folders: ["/next-app"],
  },
  NEXT_PUBLIC_GA4_ID: {
    class: "plain",
    client: true,
  },
  SUPER_SECRET_VALUE: {
    class: "secret",
  },
});
