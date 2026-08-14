import { beacon } from "./beacon.js";

export interface Pg {
  id: string;
  kind: "postgres";
}

export function postgres(id: string): Pg {
  if (process.env.OCEL_PHASE === "discovery") {
    beacon({ type: "declare", kind: "postgres", id, stack: new Error().stack });
  }
  return { id, kind: "postgres" };
}
