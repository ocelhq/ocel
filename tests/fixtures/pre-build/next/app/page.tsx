import { db } from "../infra/db";

export const dynamic = "force-static";

export default async function Home() {
  const { rows } = await db.query<{ title: string; at: string }>(
    "SELECT title, now()::text AS at FROM notes ORDER BY id",
  );
  return (
    <main>
      <p id="at">prerendered at {rows[0]?.at}</p>
      <ul>
        {rows.map((row) => (
          <li key={row.title}>prerendered note {row.title}</li>
        ))}
      </ul>
    </main>
  );
}
