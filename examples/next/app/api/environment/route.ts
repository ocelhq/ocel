import { connection } from "next/server";
import { env } from "../../../ocel/vars";

export async function GET() {
  await connection();
  return Response.json({ scoped: env.SOME_FOLDER_VALUE });
}
