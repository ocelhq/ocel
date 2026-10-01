import { realtime } from "ocel/realtime";
import { z } from "zod";

export type Caller = { id: string; orders: string[]; projects: string[]; mayPublish: boolean };

function readList(value: string | null): string[] {
  return value ? value.split(",") : [];
}

export function readCaller(request: Request): Caller | null {
  const credential = request.headers.get("authorization")?.match(/^Bearer (.+)$/)?.[1];
  if (!credential) {
    return null;
  }
  const claims = new URLSearchParams(credential);
  const id = claims.get("user");
  if (!id) {
    return null;
  }
  return {
    id,
    orders: readList(claims.get("orders")),
    projects: readList(claims.get("projects")),
    mayPublish: claims.get("publish") === "yes",
  };
}

const Note = z.object({ text: z.string() });

export const rt = realtime("app", {
  authorize: readCaller,
  channels: {
    "orders/:orderId": {
      schema: z.object({ status: z.string() }),
      subscribe: ({ auth, params }) => auth.orders.includes(params.orderId),
    },
    "projects/:projectId/deploys/:deployId": {
      schema: z.object({ state: z.string() }),
      wildcard: true,
      subscribe: ({ auth, params }) =>
        params.projectId !== undefined && auth.projects.includes(params.projectId),
    },
    "rooms/:roomId": {
      schema: Note,
      subscribe: () => true,
      publish: ({ auth }) => auth.mayPublish,
    },
    status: { schema: Note, subscribe: "public" },
  },
});
