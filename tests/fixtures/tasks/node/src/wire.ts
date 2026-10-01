import express, { Router } from "express";
import { runs } from "ocel/task";
import { notices, type Sighting, sighting, verbatim } from "../infra/wire";
import { serveJSON } from "./serve";

type RawJSON = { rawJSON(text: string): unknown };

type SourceReviver = (key: string, value: unknown, context: { source: string }) => unknown;

function parseKeepingNumberText(text: string): unknown {
  const keepSource: SourceReviver = (_key, value, { source }) =>
    typeof value === "number" ? (JSON as unknown as RawJSON).rawJSON(source) : value;
  return JSON.parse(text, keepSource as Parameters<typeof JSON.parse>[1]);
}

export const wire = Router();

wire.use(express.text({ type: () => true }));

wire.post(
  "/trigger",
  serveJSON(async (req) => {
    const { id } = await verbatim.trigger(parseKeepingNumberText(String(req.body)));
    return { id };
  }),
);

wire.post(
  "/send",
  serveJSON(async (req) => {
    const messageId = await notices.send(parseKeepingNumberText(String(req.body)));
    return { messageId };
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
