import { describe, expect, it } from "bun:test";
import type { Check } from "./contract";
import { legsDriven, stepsPlanned } from "./lifecycle";
import { fixture, LIVES, SERVES } from "./matrix/types";
import type { Deployment } from "./targets/types";

const ping: Check = { title: "ping", run: async () => undefined };
const living = fixture("lifecycle/next", {
  apps: ["web"],
  legs: LIVES,
  checks: [ping],
  on: { aws: { base: true } },
});
const cell = { name: "lifecycle/next", fixture: living };
const serving = {
  legs: SERVES,
  steps: [
    { app: "web", title: "up", leg: "up" as const },
    { app: "web", title: "ping", leg: "contract" as const },
    { app: "web", title: "destroy", leg: "destroy" as const },
  ],
};

describe("the steps a cell process runs", () => {
  it("runs the steps the plan printed, in its order", () => {
    expect(stepsPlanned(cell, serving).map((step) => step.title)).toEqual([
      "up",
      "ping",
      "destroy",
    ]);
  });

  it("refuses a plan whose steps the lifecycle no longer walks", () => {
    const drifted = {
      legs: SERVES,
      steps: [
        { app: "web", title: "up", leg: "up" as const },
        { app: "web", title: "pong", leg: "contract" as const },
        { app: "web", title: "destroy", leg: "destroy" as const },
      ],
    };
    expect(() => stepsPlanned(cell, drifted)).toThrow(
      /lifecycle\/next walks web · up, web · ping, web · destroy, not the planned web · up, web · pong, web · destroy/,
    );
  });
});

describe("the legs a target drives", () => {
  const replaced = async (): Promise<Deployment> => ({
    baseUrl: () => "",
    fetch: async () => new Response(),
  });

  it("accepts legs the target has a method for", () => {
    expect(() =>
      legsDriven({ name: "aws", redeploy: replaced, rollback: replaced }, LIVES),
    ).not.toThrow();
    expect(() => legsDriven({ name: "dev" }, SERVES)).not.toThrow();
  });

  it("refuses a redeploy or rollback leg the target cannot drive", () => {
    expect(() => legsDriven({ name: "dev" }, LIVES)).toThrow(
      /dev walks redeploy, rollback without a method for it/,
    );
    expect(() => legsDriven({ name: "vps", redeploy: replaced }, LIVES)).toThrow(
      /vps walks rollback without a method for it/,
    );
  });
});
