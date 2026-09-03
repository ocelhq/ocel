import { pg } from "../ocel/index";

export type StateRow = {
  key: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
  value: string | null;
};

export async function bump(key: string, value?: string): Promise<number> {
  const { rows } = await pg.query<{ count: number }>(
    `INSERT INTO next_state (key, count, value) VALUES ($1, 1, $2)
     ON CONFLICT (key) DO UPDATE
       SET count = next_state.count + 1,
           last_seen = now(),
           value = COALESCE(EXCLUDED.value, next_state.value)
     RETURNING count`,
    [key, value ?? null],
  );
  return rows[0].count;
}

export async function readState(): Promise<StateRow[]> {
  const { rows } = await pg.query<StateRow>(
    `SELECT key,
            count,
            first_seen AS "firstSeen",
            last_seen  AS "lastSeen",
            value
       FROM next_state
      ORDER BY key`,
  );
  return rows;
}
