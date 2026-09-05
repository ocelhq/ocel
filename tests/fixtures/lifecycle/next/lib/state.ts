import { pg } from "../ocel/index";

export type StateRow = {
  key: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
};

export async function bump(key: string): Promise<number> {
  const { rows } = await pg.query<{ count: number }>(
    `INSERT INTO next_state (key, count) VALUES ($1, 1)
     ON CONFLICT (key) DO UPDATE
       SET count = next_state.count + 1,
           last_seen = now()
     RETURNING count`,
    [key],
  );
  const [row] = rows;
  if (!row) {
    throw new Error(`next_state returned no row for ${key}`);
  }
  return row.count;
}

export async function readState(keys: string[]): Promise<StateRow[]> {
  const { rows } = await pg.query<StateRow>(
    `SELECT key,
            count,
            first_seen AS "firstSeen",
            last_seen  AS "lastSeen"
       FROM next_state
      WHERE key = ANY($1)
      ORDER BY key`,
    [keys],
  );
  return rows;
}
