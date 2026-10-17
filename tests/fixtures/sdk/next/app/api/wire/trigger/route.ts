import { JsonText } from "ocel/task";
import { verbatim } from "../../../../infra/wire";

export const runtime = "nodejs";

export async function POST(request: Request) {
  const { id } = await verbatim.trigger(new JsonText(await request.text()));
  return Response.json({ id });
}
