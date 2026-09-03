import type { Leg } from "../spec";
import { suiteTitles } from "./rows";
import type { Expectations } from "./types";

const UPLOAD_KEY = "https://github.com/ocelhq/ocel/issues/882";
const NO_PROJECT_DELETE = "https://github.com/ocelhq/ocel/issues/877";
const ENV_SET_NEEDS_PROVIDER = "https://github.com/ocelhq/ocel/issues/881";
const NO_ROUTER_IN_FRONT = "https://github.com/ocelhq/ocel/issues/898";

const DEV_CONTRACT_LEGS: Leg[] = ["contract"];

export const dev: Expectations = {
  "express/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "hono/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "fastify/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "next/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    ...suiteTitles("next-cache", DEV_CONTRACT_LEGS, NO_ROUTER_IN_FRONT),
    destroy: NO_PROJECT_DELETE,
  },
  "hello-express/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    destroy: NO_PROJECT_DELETE,
  },
  "hello-next/web": {
    up: ENV_SET_NEEDS_PROVIDER,
    destroy: NO_PROJECT_DELETE,
  },
  "workspace/next": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "workspace/express": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "workspace/hono": {
    up: ENV_SET_NEEDS_PROVIDER,
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
};
