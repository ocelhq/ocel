import type { Connector } from "@console/db/schema";

const onlineWithin = 90_000;

export type Liveness = "online" | "offline" | "never";

export function liveness(held: Pick<Connector, "connectedAt" | "lastSeenAt">): Liveness {
  if (held.connectedAt === null) {
    return "never";
  }
  if (held.lastSeenAt === null) {
    return "offline";
  }
  return Date.now() - held.lastSeenAt.getTime() < onlineWithin ? "online" : "offline";
}
