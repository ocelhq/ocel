import { defineEnv } from "ocel/env";
import { z } from "zod";

export const env = defineEnv({
  APP_ID: {
    class: "plain",
    schema: z.string(),
  },
  PRIVATE_KEY: {
    class: "secret",
  },
  WEBHOOK_SECRET: {
    class: "secret",
  },
  REPORT_SECRET: {
    class: "secret",
  },
});
