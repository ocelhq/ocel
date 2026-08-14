import { db } from "../../../shared/index.js";

export async function run() {
  const { processJobs } = await import("./jobs.js");
  return { db, jobs: processJobs() };
}
