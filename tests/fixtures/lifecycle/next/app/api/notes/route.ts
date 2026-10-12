import { pg } from "../../../infra/index";

export const dynamic = "force-dynamic";

export async function GET() {
  const { rows } = await pg.query<{ title: string }>("SELECT title FROM notes ORDER BY id");
  return Response.json({ titles: rows.map((row) => row.title) });
}
