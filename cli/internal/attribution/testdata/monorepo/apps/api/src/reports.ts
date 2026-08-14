import { db } from "../../../shared/db.js";

export function report() {
  return db.id;
}
