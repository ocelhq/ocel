import { pg } from "@console/resources";
import { drizzle } from "drizzle-orm/node-postgres";
import * as schema from "./schema";

export const db = drizzle(pg, { schema });
