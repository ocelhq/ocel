import { defineEnv } from "ocel/env";
import { z } from "zod";

export const env = defineEnv({
  GREETING: {
    class: "plain",
    schema: z.string().default("hello"),
  },
});
