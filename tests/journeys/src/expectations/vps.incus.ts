import { CONTRACT_LEGS, suiteTitles } from "./keys";
import type { Expectations } from "./types";

const NO_ROUTER_ON_A_BOX = "https://github.com/ocelhq/ocel/issues/900";
const GREETING_DECLARED_TWICE = "https://github.com/ocelhq/ocel/issues/907";

export const vpsIncus: Expectations = {
  "next/web": {
    ...suiteTitles("next-cache", CONTRACT_LEGS, NO_ROUTER_ON_A_BOX),
  },
  "workspace/next": {
    up: GREETING_DECLARED_TWICE,
  },
  "workspace/express": {
    up: GREETING_DECLARED_TWICE,
  },
  "workspace/hono": {
    up: GREETING_DECLARED_TWICE,
  },
};
