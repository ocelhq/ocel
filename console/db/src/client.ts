import { drizzle } from "drizzle-orm/node-postgres";
import { pg } from "@console/resources";
import * as schema from "./schema";

export const db = drizzle(pg, { schema });
