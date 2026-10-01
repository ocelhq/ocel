import { z } from "zod";
import { realtime } from "../realtime/index.js";

export const rt = realtime("app", {
  authorize: async (request) => {
    const session = request.headers.get("authorize-marker-7d1c");
    return session ? { id: session } : null;
  },
  channels: {
    "orders/:orderId": {
      schema: z.object({ status: z.string() }),
      subscribe: ({ auth }) => auth.id === "subscribe-rule-marker-41af",
      publish: ({ body }) => body.status !== "publish-rule-marker-9b20",
    },
  },
});
