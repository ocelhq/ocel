import { expect, test } from "vitest";
import {
  type DeploymentStatusInput,
  environmentName,
  MARKER,
  type PreviewClient,
  type PreviewComment,
  type PreviewDeployment,
  type PreviewInput,
  reconcile,
} from "../src/preview";
import type { DeployResult } from "../src/report";

const SHA = "0123456789abcdef0123456789abcdef01234567";

function fakeClient() {
  const deployments: PreviewDeployment[] = [];
  const statuses: DeploymentStatusInput[] = [];
  const comments: PreviewComment[] = [];
  const created: string[] = [];
  const updated: string[] = [];
  let nextId = 1;

  const client: PreviewClient = {
    async listDeployments({ environment, sha }) {
      return deployments.filter(
        (deployment) =>
          deployment.environment === environment && (sha === undefined || deployment.sha === sha),
      );
    },
    async createDeployment({ ref, environment }) {
      const deployment = { id: nextId++, environment, sha: ref };
      deployments.push(deployment);
      return deployment;
    },
    async createDeploymentStatus(status) {
      statuses.push(status);
    },
    async listComments() {
      return comments;
    },
    async createComment({ pr, body }) {
      comments.push({ id: 100 + pr, body, fromApp: true });
      created.push(body);
    },
    async updateComment({ commentId, body }) {
      const comment = comments.find((candidate) => candidate.id === commentId);
      if (comment) comment.body = body;
      updated.push(body);
    },
  };

  return { client, deployments, statuses, comments, created, updated };
}

const base: PreviewInput = {
  repo: "ocelhq/ocel",
  pr: 7,
  sha: SHA,
  ref: "feature/preview",
  runUrl: "https://github.com/ocelhq/ocel/actions/runs/42",
  phase: "started",
};

const result: DeployResult = {
  schemaVersion: 1,
  slug: "ocelhq",
  environment: { class: "preview" },
  promotionId: "prom_1",
  apps: [{ name: "web", urls: ["https://web.preview.example"] }],
  deployedAt: "2026-09-06T10:11:12Z",
};

test("started then deployed reuses one deployment and updates one comment", async () => {
  const fake = fakeClient();

  await reconcile(fake.client, base);
  await reconcile(fake.client, { ...base, phase: "deployed", result });

  expect(fake.deployments).toHaveLength(1);
  expect(fake.deployments[0]?.environment).toBe(environmentName(7));
  expect(fake.statuses.map((status) => status.deploymentId)).toEqual([1, 1]);
  expect(fake.statuses[0]).toMatchObject({
    state: "in_progress",
    logUrl: base.runUrl,
  });
  expect(fake.statuses[1]).toMatchObject({
    state: "success",
    environmentUrl: "https://web.preview.example",
    logUrl: base.runUrl,
    autoInactive: true,
  });

  expect(fake.created).toHaveLength(1);
  expect(fake.updated).toHaveLength(1);
  expect(fake.comments).toHaveLength(1);
  expect(fake.comments[0]?.body).toContain(MARKER);
  expect(fake.comments[0]?.body).toContain("https://web.preview.example");
});

test("failed posts a failure status", async () => {
  const fake = fakeClient();

  await reconcile(fake.client, { ...base, phase: "failed", error: "build exited 1" });

  expect(fake.statuses).toEqual([{ deploymentId: 1, state: "failure", logUrl: base.runUrl }]);
  expect(fake.created[0]).toContain("build exited 1");
});

test("removed inactivates every deployment in the environment", async () => {
  const fake = fakeClient();

  await reconcile(fake.client, base);
  await reconcile(fake.client, { ...base, sha: "b".repeat(40) });
  fake.statuses.length = 0;

  await reconcile(fake.client, { ...base, phase: "removed", runUrl: undefined });

  expect(fake.deployments).toHaveLength(2);
  expect(fake.statuses).toEqual([
    { deploymentId: 1, state: "inactive" },
    { deploymentId: 2, state: "inactive" },
  ]);
  expect(fake.comments[0]?.body).toContain("torn down");
});

test("a comment from someone else is not treated as the sticky comment", async () => {
  const fake = fakeClient();
  fake.comments.push({ id: 1, body: `noise ${MARKER}`, fromApp: false });

  await reconcile(fake.client, base);

  expect(fake.created).toHaveLength(1);
  expect(fake.updated).toHaveLength(0);
});
