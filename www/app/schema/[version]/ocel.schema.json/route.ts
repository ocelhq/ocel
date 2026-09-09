import { readFile } from "node:fs/promises";
import { join } from "node:path";

async function shipped(): Promise<{ document: string; version: string }> {
  const document = await readFile(
    join(process.cwd(), "public", "schema", "ocel.schema.json"),
    "utf8",
  );
  const id: string = JSON.parse(document).$id;
  return { document, version: id.split("/").at(-2) ?? "" };
}

export async function GET(
  _request: Request,
  { params }: { params: Promise<{ version: string }> },
): Promise<Response> {
  const { version } = await params;
  const { document, version: serves } = await shipped();
  if (version !== serves) {
    return new Response(`no schema for version ${version}; this site serves ${serves}`, {
      status: 404,
      headers: { "content-type": "text/plain; charset=utf-8" },
    });
  }
  return new Response(document, {
    headers: {
      "content-type": "application/schema+json; charset=utf-8",
      "cache-control": "public, max-age=3600",
    },
  });
}
