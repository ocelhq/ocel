import { migrate } from "../../../ocel/index";

export async function POST() {
  await migrate();
  return new Response(null, { status: 204 });
}
