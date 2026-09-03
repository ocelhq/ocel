import { readState } from "../../../../lib/state";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const keys = new URL(request.url).searchParams.getAll("key");
  if (keys.length === 0) {
    return Response.json({ error: "name the keys to read back with ?key=" }, { status: 400 });
  }
  return Response.json({ rows: await readState(keys) });
}
