import type { Connector } from "@console/db/schema";

const onlineWithin = 90_000;

export type Liveness = "online" | "offline" | "never";

export function liveness(row: Pick<Connector, "connectedAt" | "lastSeenAt">): Liveness {
  if (row.connectedAt === null) {
    return "never";
  }
  if (row.lastSeenAt === null) {
    return "offline";
  }
  return Date.now() - row.lastSeenAt.getTime() < onlineWithin ? "online" : "offline";
}
