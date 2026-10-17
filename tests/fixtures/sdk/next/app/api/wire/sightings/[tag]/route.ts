import { runs } from "ocel/task";
import { type Sighting, sighting } from "../../../../../infra/wire";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(_request: Request, { params }: { params: Promise<{ tag: string }> }) {
  const page = await runs.list({ task: sighting, tags: [(await params).tag], status: "COMPLETED" });
  return Response.json(page.runs.map((run) => run.output as Sighting));
}
