import type { Expectations } from "./types";

const UPLOAD_KEY = "https://github.com/ocelhq/ocel/issues/882";
const NO_PROJECT_DELETE = "https://github.com/ocelhq/ocel/issues/877";
const ENV_SET_NEEDS_PROVIDER = "https://github.com/ocelhq/ocel/issues/881";

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
    destroy: NO_PROJECT_DELETE,
  },
};
