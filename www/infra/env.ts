import { defineEnv, group } from "ocel/env/next";

export const env = defineEnv({
  NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN: { class: "plain" },
  NEXT_PUBLIC_POSTHOG_HOST: { class: "plain" },
  resend: group(
    {
      RESEND_API_KEY: { class: "secret" },
      RESEND_SEGMENT_ID: { class: "plain" },
    },
    {
      optional: true,
      description: "Adds each waitlist sign-up to this Resend segment, so it can be emailed.",
    },
  ),
});
