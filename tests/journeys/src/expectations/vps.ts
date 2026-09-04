import { CONTRACT_LEGS, suiteTitles } from "./keys";
import type { Expectations } from "./types";

const NO_ROUTER_ON_A_BOX = "https://github.com/ocelhq/ocel/issues/900";
const GREETING_DECLARED_TWICE = "https://github.com/ocelhq/ocel/issues/907";
const NO_BUCKET_ON_A_BOX = "https://github.com/ocelhq/ocel/issues/918";

export const vps: Expectations = {
  "express/web": {
    up: NO_BUCKET_ON_A_BOX,
  },
  "hono/web": {
    up: NO_BUCKET_ON_A_BOX,
  },
  "fastify/web": {
    up: NO_BUCKET_ON_A_BOX,
  },
  "next/web": {
    up: NO_BUCKET_ON_A_BOX,
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
