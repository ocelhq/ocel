import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { githubRuns, livelyRuns, runIdOf, type Verdict } from "./runs";

const ENV = { GITHUB_REPOSITORY: "ocelhq/ocel", GITHUB_TOKEN: "token" };

function answering(answers: Response[] | ((url: string) => Response | Promise<never>)) {
  const asked: string[] = [];
  const served = Array.isArray(answers) ? answers : undefined;
  let at = 0;
  const fetching = (async (input: unknown) => {
    asked.push(String(input));
    if (served) {
      const answer = served[at] ?? served[served.length - 1];
      at += 1;
      return answer;
    }
    return (answers as (url: string) => Response)(String(input));
  }) as unknown as typeof fetch;
  return { asked, fetching };
}

function run(status: string): Response {
  return new Response(JSON.stringify({ status }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

describe("runIdOf", () => {
  it("reads the run id out of a harness slug", () => {
    assert.equal(runIdOf("j-1874-deploy-node"), "1874");
  });

  it("reads the run id out of a namespace, which carries the same head", () => {
    assert.equal(runIdOf("j-19283746-deploy-next-a1b2c3"), "19283746");
  });

  it("reads no run id out of a local run, which nothing else is using", () => {
    assert.equal(runIdOf("j-local-vndaba-deploy-node"), undefined);
  });

  it("reads no run id out of a name the harness never made", () => {
    assert.equal(runIdOf("deploy-node"), undefined);
    assert.equal(runIdOf("j--deploy-node"), undefined);
    assert.equal(runIdOf("j-1874"), undefined);
  });
});

describe("livelyRuns", () => {
  const verdicts: Record<string, Verdict> = {
    "1": { standing: "live" },
    "2": { standing: "done" },
    "3": { standing: "unknown", reason: "github answered 500" },
  };

  it("keeps a live run and an unreadable one, and reclaims a finished one", async () => {
    const { keep, unreadable } = await livelyRuns(["1", "2", "3"], async (id) => verdicts[id]);
    assert.deepEqual([...keep].sort(), ["1", "3"]);
    assert.deepEqual(unreadable, [{ id: "3", reason: "github answered 500" }]);
  });

  it("asks about a run named twice once", async () => {
    const asked: string[] = [];
    await livelyRuns(["1", "1"], async (id) => {
      asked.push(id);
      return verdicts[id];
    });
    assert.deepEqual(asked, ["1"]);
  });
});

describe("githubRuns", () => {
  it("reads a queued, in-progress or waiting run as live", async () => {
    for (const status of ["queued", "in_progress", "waiting", "requested", "pending"]) {
      const look = githubRuns(ENV, answering([run(status)]).fetching);
      assert.deepEqual(await look("1874"), { standing: "live" });
    }
  });

  it("reads a completed run as done", async () => {
    const look = githubRuns(ENV, answering([run("completed")]).fetching);
    assert.deepEqual(await look("1874"), { standing: "done" });
  });

  it("reads a run github never had as done", async () => {
    const look = githubRuns(ENV, answering([new Response(null, { status: 404 })]).fetching);
    assert.deepEqual(await look("1874"), { standing: "done" });
  });

  it("reads a refusal as unknown, so what the run holds is kept", async () => {
    const look = githubRuns(ENV, answering([new Response(null, { status: 401 })]).fetching);
    assert.deepEqual(await look("1874"), { standing: "unknown", reason: "github answered 401" });
  });

  it("reads a network failure as unknown", async () => {
    const look = githubRuns(
      ENV,
      answering(() => Promise.reject(new Error("getaddrinfo ENOTFOUND"))).fetching,
    );
    const verdict = await look("1874");
    assert.equal(verdict.standing, "unknown");
    assert.match(verdict.reason ?? "", /ENOTFOUND/);
  });

  it("reads every run as unknown when the environment names no token", async () => {
    const look = githubRuns(
      { GITHUB_REPOSITORY: "ocelhq/ocel" },
      answering([run("queued")]).fetching,
    );
    assert.equal((await look("1874")).standing, "unknown");
  });

  it("asks github about a run once and holds the answer", async () => {
    const { asked, fetching } = answering([run("in_progress")]);
    const look = githubRuns(ENV, fetching);
    await look("1874");
    await look("1874");
    assert.deepEqual(asked, ["https://api.github.com/repos/ocelhq/ocel/actions/runs/1874"]);
  });
});
