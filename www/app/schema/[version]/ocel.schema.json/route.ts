import { readFile } from "node:fs/promises";
import { join } from "node:path";

export async function GET(): Promise<Response> {
  const schema = await readFile(
    join(process.cwd(), "public", "schema", "ocel.schema.json"),
    "utf8",
  );
  return new Response(schema, {
    headers: {
      "content-type": "application/schema+json; charset=utf-8",
      "cache-control": "public, max-age=3600",
    },
  });
}
