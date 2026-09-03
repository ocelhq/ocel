import type { Expectations } from "./types";

const UPLOAD_KEY = "https://github.com/ocelhq/ocel/issues/882";
const NO_PROJECT_DELETE = "https://github.com/ocelhq/ocel/issues/877";

export const dev: Expectations = {
  "express/web": {
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "hono/web": {
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "fastify/web": {
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
  "next/web": {
    "the upload protocol stores a document and /api/documents lists it": UPLOAD_KEY,
    destroy: NO_PROJECT_DELETE,
  },
};
