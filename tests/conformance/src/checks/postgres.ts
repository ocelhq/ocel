import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkPostgres: Check = ({ baseUrl, runId }) => {
  it("creates, lists, gets, updates, and deletes a todo", async () => {
    const todos = `${baseUrl()}/api/todos`;
    const title = `write conformance tests ${runId}`;
    const created = await fetch(todos, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ title }),
    });
    expect(created.status).toBe(201);
    const todo = (await created.json()) as {
      id: number;
      title: string;
      done: boolean;
    };
    expect(todo).toEqual({ id: expect.any(Number), title, done: false });

    const listed = await fetch(todos);
    expect(listed.status).toBe(200);
    const all = (await listed.json()) as typeof todo[];
    expect(all.find((candidate) => candidate.id === todo.id)).toEqual(todo);

    const got = await fetch(`${todos}/${todo.id}`);
    expect(got.status).toBe(200);
    expect(await got.json()).toEqual(todo);

    const updatedTodo = { id: todo.id, title: `ship ${runId}`, done: true };
    const updated = await fetch(`${todos}/${todo.id}`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(updatedTodo),
    });
    expect(updated.status).toBe(200);
    expect(await updated.json()).toEqual(updatedTodo);

    const gotUpdated = await fetch(`${todos}/${todo.id}`);
    expect(gotUpdated.status).toBe(200);
    expect(await gotUpdated.json()).toEqual(updatedTodo);

    const deleted = await fetch(`${todos}/${todo.id}`, { method: "DELETE" });
    expect(deleted.status).toBe(204);
    expect((await fetch(`${todos}/${todo.id}`)).status).toBe(404);
  });
};
