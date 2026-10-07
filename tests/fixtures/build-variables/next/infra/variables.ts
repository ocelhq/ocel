import { defineEnv } from "ocel/env";

export const env = defineEnv({
  BUILD_SENSITIVE: {
    class: "sensitive",
  },
  BUILD_SECRET: {
    class: "secret",
  },
});
