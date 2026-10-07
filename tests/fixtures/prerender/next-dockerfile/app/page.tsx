import { db } from "../infra/db";

export const dynamic = "force-static";

export default async function Home() {
  const { rows } = await db.query<{ database: string; at: string }>(
    "SELECT current_database() AS database, now()::text AS at",
  );
  const [read] = rows;
  return (
    <main>
      <p id="database">prerendered from database {read?.database}</p>
      <p id="at">prerendered at {read?.at}</p>
    </main>
  );
}
