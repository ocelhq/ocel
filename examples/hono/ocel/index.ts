import { postgres } from "ocel/postgres";

// Declaring the resource *is* the provisioning step: `ocel dev` discovers this
// call, provisions a Postgres database for it, and injects the connection into
// the app's environment. Everything else in this example reads the pool from
// here.
export const pg = postgres("main");
