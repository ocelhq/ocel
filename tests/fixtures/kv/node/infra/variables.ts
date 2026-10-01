import { defineEnv } from "ocel/env";

export const env = defineEnv({
  PASSWORD_REPORT_NONCE: {
    class: "secret",
  },
});
