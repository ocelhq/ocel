import { defineEnv } from "ocel/env";

export const env = defineEnv({
  JOURNEY_NONCE: {
    class: "secret",
  },
});
