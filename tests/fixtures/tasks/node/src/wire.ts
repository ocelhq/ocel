import express, { Router } from "express";
import { runs } from "ocel/task";
import {
  countRequests,
  envelope,
  listRequestsSince,
  notices,
  type RecordedEnvelope,
  readNames,
  type Sighting,
  sighting,
  verbatim,
} from "../infra/wire";
import { serveJSON } from "./serve";

type ExactJSON = { rawJSON(text: string): unknown };

type SourceReviver = (key: string, value: unknown, context: { source: string }) => unknown;

function parseExactly(text: string): unknown {
  const keepSource: SourceReviver = (_key, value, { source }) =>
    typeof value === "number" ? (JSON as unknown as ExactJSON).rawJSON(source) : value;
  return JSON.parse(text, keepSource as Parameters<typeof JSON.parse>[1]);
}

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export const wire = Router();

wire.use(express.text({ type: () => true }));

wire.get(
  "/names",
  serveJSON(async () => readNames()),
);

wire.post(
  "/trigger",
  serveJSON(async (req) => {
    const since = countRequests();
    try {
      const { id } = await verbatim.trigger(parseExactly(String(req.body)));
      return { id, error: "", requests: listRequestsSince(since) };
    } catch (error) {
      return { id: "", error: describeError(error), requests: listRequestsSince(since) };
    }
  }),
);

wire.post(
  "/send",
  serveJSON(async (req) => {
    const since = countRequests();
    try {
      const messageId = await notices.send(parseExactly(String(req.body)));
      return { messageId, error: "", requests: listRequestsSince(since) };
    } catch (error) {
      return { messageId: "", error: describeError(error), requests: listRequestsSince(since) };
    }
  }),
);

wire.get(
  "/runs/:id",
  serveJSON(async (req) => {
    const run = await runs.retrieve(String(req.params.id));
    return {
      id: run.id,
      task: run.task,
      status: run.status,
      payload: JSON.stringify(run.payload),
      output: JSON.stringify(run.output),
      error: run.error ?? "",
    };
  }),
);

wire.get(
  "/sightings/:tag",
  serveJSON(async (req) => {
    const page = await runs.list({
      task: sighting,
      tags: [String(req.params.tag)],
      status: "COMPLETED",
    });
    return page.runs.map((run) => run.output as Sighting);
  }),
);

wire.get(
  "/envelopes/:tag",
  serveJSON(async (req) => {
    const page = await runs.list({
      task: envelope,
      tags: [String(req.params.tag)],
      status: "COMPLETED",
    });
    return page.runs.map((run) => (run.output as RecordedEnvelope).recordedEnvelope);
  }),
);
