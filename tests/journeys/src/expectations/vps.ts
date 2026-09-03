import { CONTRACT_LEGS, suiteTitles } from "./keys";
import type { Expectations } from "./types";

const CONTAINER_IN_A_WORKSPACE = "https://github.com/ocelhq/ocel/issues/885";
const NO_ROUTER_ON_A_BOX = "https://github.com/ocelhq/ocel/issues/900";
const GREETING_DECLARED_TWICE = "https://github.com/ocelhq/ocel/issues/907";

export const vps: Expectations = {
  "express/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "hono/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "fastify/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "next/web": {
    up: CONTAINER_IN_A_WORKSPACE,
    ...suiteTitles("next-cache", CONTRACT_LEGS, NO_ROUTER_ON_A_BOX),
  },
  "hello-express/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "hello-next/web": {
    up: CONTAINER_IN_A_WORKSPACE,
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
