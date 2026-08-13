import { serve } from "@hono/node-server";

import { MARKER, app } from "./app.js";

const PORT = Number(process.env.PORT ?? 3302);

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
