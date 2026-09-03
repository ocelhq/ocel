import type { Expectations } from "./types";

const CONTAINER_IN_A_WORKSPACE = "https://github.com/ocelhq/ocel/issues/885";

export const vpsIncus: Expectations = {
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
  },
  "hello-express/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "hello-next/web": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "workspace/next": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "workspace/express": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "workspace/hono": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
};
