import { beacon } from "./beacon.js";

export interface Bucket {
  id: string;
  kind: "bucket";
}

export function bucket(id: string): Bucket {
  if (process.env.OCEL_PHASE === "discovery") {
    beacon({ type: "declare", kind: "bucket", id, stack: new Error().stack });
  }
  return { id, kind: "bucket" };
}
