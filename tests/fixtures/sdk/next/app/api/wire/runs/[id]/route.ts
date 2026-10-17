import { runs } from "ocel/task";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(_request: Request, { params }: { params: Promise<{ id: string }> }) {
  const run = await runs.retrieve((await params).id);
  return Response.json({
    id: run.id,
    task: run.task,
    status: run.status,
    payload: run.payloadJson.text,
    output: run.outputJson?.text ?? "",
    error: run.error ?? "",
  });
}
