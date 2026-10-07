import { db } from "../../../infra/db";

export const dynamicParams = false;

export async function generateStaticParams() {
  const { rows } = await db.query<{ n: number }>("SELECT generate_series(1, 3) AS n");
  return rows.map((row) => ({ n: String(row.n) }));
}

export default async function Row({ params }: { params: Promise<{ n: string }> }) {
  const { n } = await params;
  const { rows } = await db.query<{ square: number }>("SELECT ($1::int * $1::int) AS square", [n]);
  return (
    <p id="row">
      row {n} squared is {rows[0]?.square}
    </p>
  );
}
