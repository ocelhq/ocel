import type { Leg } from "../spec";
import { suiteTitles } from "./rows";
import type { Expectations } from "./types";

const CONTAINER_IN_A_WORKSPACE = "https://github.com/ocelhq/ocel/issues/885";
const NO_ROUTER_ON_A_BOX = "https://github.com/ocelhq/ocel/issues/900";

const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

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
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "workspace/express": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
  "workspace/hono": {
    up: CONTAINER_IN_A_WORKSPACE,
  },
};
