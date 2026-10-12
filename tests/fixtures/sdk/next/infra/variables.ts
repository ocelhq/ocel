import { defineEnv } from "ocel/env/next";
import { z } from "zod";

export const env = defineEnv({
  GREETING: {
    class: "plain",
    schema: z.string().default("hello"),
  },
  SECRET_TOKEN: {
    class: "secret",
  },
  BUILD_SENSITIVE: {
    class: "sensitive",
  },
  BUILD_SECRET: {
    class: "secret",
  },
  NEXT_PUBLIC_GREETING: { class: "plain", schema: z.string().startsWith("journey-") },
  SENSITIVE_TOKEN: { class: "sensitive" },
});
