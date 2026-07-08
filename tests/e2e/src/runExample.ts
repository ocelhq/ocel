import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import {
  base,
  deletePlaceholderConfig,
  type DevHandle,
  type ExampleSpec,
  resetPlaceholderConfig,
  runInit,
  runMigrate,
  startDev,
  waitForHealth,
} from "./harness";

// Drives one example end to end through the real CLI:
//   init (fresh project) -> run (migrate) -> dev (serve) -> CRUD over HTTP.
// Each example gets its own project, hence its own provisioned database, so
// the three specs are safe to run in parallel.
export function describeExample(spec: ExampleSpec) {
  describe(`${spec.framework} example (e2e)`, () => {
    const token = inject("accessToken");
    // A per-run id keeps CreateProject's slug unique across reruns (409 on
    // repeat otherwise), while staying stable within a single run.
    const runId = `${Date.now().toString(36)}-${Math.random()
      .toString(36)
      .slice(2, 7)}`;
    let dev: DevHandle | undefined;

    beforeAll(async () => {
      await deletePlaceholderConfig(spec);
      await runInit(spec, token, runId);
      await runMigrate(spec, token);
      dev = startDev(spec, token);
      await waitForHealth(spec);
    }, 180_000);

    afterAll(async () => {
      await dev?.stop();
      await resetPlaceholderConfig(spec);
    });

    it("creates, lists, gets, and deletes a todo", async () => {
      const todos = `${base(spec)}${spec.todosPath}`;

      // create
      const created = await fetch(todos, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ title: "write e2e tests" }),
      });
      expect(created.status).toBe(201);
      const todo = (await created.json()) as {
        id: number;
        title: string;
        done: boolean;
      };
      expect(todo.title).toBe("write e2e tests");
      expect(todo.done).toBe(false);
      expect(typeof todo.id).toBe("number");

      // list
      const listed = await fetch(todos);
      expect(listed.status).toBe(200);
      const all = (await listed.json()) as Array<{ id: number }>;
      expect(all.some((t) => t.id === todo.id)).toBe(true);

      // get one
      const got = await fetch(`${todos}/${todo.id}`);
      expect(got.status).toBe(200);
      const gotBody = (await got.json()) as { id: number; title: string };
      expect(gotBody.id).toBe(todo.id);
      expect(gotBody.title).toBe("write e2e tests");

      // delete
      const deleted = await fetch(`${todos}/${todo.id}`, {
        method: "DELETE",
      });
      expect(deleted.status).toBe(204);

      // verify gone
      const gone = await fetch(`${todos}/${todo.id}`);
      expect(gone.status).toBe(404);
    });
  });
}
