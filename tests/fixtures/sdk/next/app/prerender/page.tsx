import { pg } from "../../infra/index";

export const dynamic = "force-static";

export default async function PrerenderedFromPostgres() {
  const { rows } = await pg.query<{ database: string; at: string }>(
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
