import { pg } from "../ocel/index";

export type Todo = { id: number; title: string; done: boolean };

export async function createTodo(title: string): Promise<Todo> {
  const { rows } = await pg.query<Todo>(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [title],
  );
  return rows[0];
}

export async function listTodos(): Promise<Todo[]> {
  const { rows } = await pg.query<Todo>(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  return rows;
}

export async function getTodo(id: number): Promise<Todo | undefined> {
  const { rows } = await pg.query<Todo>(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [id],
  );
  return rows[0];
}

export async function updateTodo(
  id: number,
  title: string,
  done: boolean,
): Promise<Todo | undefined> {
  const { rows } = await pg.query<Todo>(
    "UPDATE todos SET title = $1, done = $2 WHERE id = $3 RETURNING id, title, done",
    [title, done, id],
  );
  return rows[0];
}

export async function deleteTodo(id: number): Promise<boolean> {
  const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [id]);
  return (rowCount ?? 0) > 0;
}
