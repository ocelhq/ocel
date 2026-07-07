import { drizzle } from "drizzle-orm/node-postgres";
import { pg } from "@repo/resources";
import * as schema from "./schema";

export const db = drizzle(pg, { schema });
