import express, { Router } from "express";
import { JsonText, runs } from "ocel/task";
import { notices, type Sighting, sighting, verbatim } from "../infra/wire";
import { serveJSON } from "./serve";

export const wire = Router();

wire.use(express.text({ type: () => true }));

wire.post(
  "/trigger",
  serveJSON(async (req) => {
    const { id } = await verbatim.trigger(new JsonText(String(req.body)));
    return { id };
  }),
);

wire.post(
  "/send",
  serveJSON(async (req) => {
    const messageId = await notices.send(new JsonText(String(req.body)));
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
      payload: run.payloadJson.text,
      output: run.outputJson?.text ?? "",
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
