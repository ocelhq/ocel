import { readState } from "../../../../lib/state";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET() {
  return Response.json({ now: new Date().toISOString(), rows: await readState() });
}
