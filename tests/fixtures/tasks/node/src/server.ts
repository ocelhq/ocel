import express, { type NextFunction, type Request, type Response } from "express";
import { type Run, type RunStatus, runs, type Task, type TriggerOptions } from "ocel/task";
import type { SendOptions, Topic } from "ocel/topic";
import { tasks, topics } from "../infra/index";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3108);

const app = express();
app.use(express.json());

type Served = (req: Request, res: Response) => Promise<unknown>;

function answer(serve: Served) {
  return (req: Request, res: Response, next: NextFunction) => {
    serve(req, res).then((body) => res.json(body), next);
  };
}

function findTask(name: string): Task {
  const found = (tasks as Record<string, Task>)[name];
  if (!found) {
    throw new RangeError(`no task named ${name}`);
  }
  return found;
}

function findTopic(name: string): Topic {
  const found = (topics as Record<string, Topic>)[name];
  if (!found) {
    throw new RangeError(`no topic named ${name}`);
  }
  return found;
}

function readTriggerOptions(raw: Record<string, unknown> = {}): TriggerOptions {
  const { dueAt, ...options } = raw;
  return {
    ...(options as TriggerOptions),
    ...(typeof dueAt === "string" ? { delay: new Date(dueAt) } : {}),
  };
}

function showRun(run: Run) {
  return {
    ...run,
    createdAt: run.createdAt?.getTime(),
    dueAt: run.dueAt?.getTime(),
    startedAt: run.startedAt?.getTime(),
    finishedAt: run.finishedAt?.getTime(),
    expiresAt: run.expiresAt?.getTime(),
  };
}

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.post(
  "/api/tasks/:task/trigger",
  answer(async (req) =>
    findTask(String(req.params.task)).trigger(
      req.body.payload,
      readTriggerOptions(req.body.options),
    ),
  ),
);

app.post(
  "/api/tasks/:task/batch-trigger",
  answer(async (req) => ({
    ids: (
      await findTask(String(req.params.task)).batchTrigger(
        (req.body.items as { payload: unknown; options?: Record<string, unknown> }[]).map(
          (item) => ({ payload: item.payload, options: readTriggerOptions(item.options) }),
        ),
      )
    ).map((handle) => handle.id),
  })),
);

app.get(
  "/api/runs/:id",
  answer(async (req) => showRun(await runs.retrieve(String(req.params.id)))),
);

app.get(
  "/api/runs",
  answer(async (req) => {
    const listed = (name: string) =>
      typeof req.query[name] === "string" ? String(req.query[name]).split(",") : undefined;
    const page = await runs.list({
      task: typeof req.query.task === "string" ? req.query.task : undefined,
      status: listed("status") as RunStatus[] | undefined,
      tags: listed("tags"),
      limit: req.query.limit === undefined ? undefined : Number(req.query.limit),
    });
    return { runs: page.runs.map(showRun), nextCursor: page.nextCursor };
  }),
);

app.post(
  "/api/runs/:id/cancel",
  answer(async (req) => showRun(await runs.cancel(String(req.params.id)))),
);

app.post(
  "/api/runs/:id/replay",
  answer(async (req) => runs.replay(String(req.params.id))),
);

app.post(
  "/api/runs/:id/reschedule",
  answer(async (req) =>
    showRun(
      await runs.reschedule(String(req.params.id), { delay: new Date(String(req.body.dueAt)) }),
    ),
  ),
);

app.post(
  "/api/topics/:topic/send",
  answer(async (req) => ({
    messageId: await findTopic(String(req.params.topic)).send(
      req.body.payload,
      (req.body.options ?? {}) as SendOptions,
    ),
  })),
);

app.get(
  "/api/topics/:topic/dead-letters/:consumer",
  answer(async (req) => {
    const letters = findTopic(String(req.params.topic)).deadLetter(String(req.params.consumer));
    const page = await letters.list();
    return { count: await letters.count(), deadLetters: page.deadLetters };
  }),
);

app.post(
  "/api/topics/:topic/dead-letters/:consumer/redrive",
  answer(async (req) => ({
    redriven: await findTopic(String(req.params.topic))
      .deadLetter(String(req.params.consumer))
      .redrive(req.body.executions ?? []),
  })),
);

app.post(
  "/api/topics/:topic/dead-letters/:consumer/purge",
  answer(async (req) => ({
    purged: await findTopic(String(req.params.topic))
      .deadLetter(String(req.params.consumer))
      .purge(req.body.executions ?? []),
  })),
);

app.use((error: unknown, _req: Request, res: Response, _next: NextFunction) => {
  const status = error instanceof RangeError ? 404 : 400;
  res.status(status).json({ error: error instanceof Error ? error.message : String(error) });
});

app.listen(PORT, () => {
  console.log(`tasks fixture listening on http://localhost:${PORT}`);
});
