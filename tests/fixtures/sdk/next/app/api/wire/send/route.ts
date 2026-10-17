import { JsonText } from "ocel/task";
import { notices } from "../../../../infra/wire";

export const runtime = "nodejs";

export async function POST(request: Request) {
  const messageId = await notices.send(new JsonText(await request.text()));
  return Response.json({ messageId });
}
