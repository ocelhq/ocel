import { db } from "@console/db";
import { deployment } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../../test/db";
import { createProject } from "../../route";
import { createDeployment, getDeployment, listDeployments } from "./route";

async function createProjectFor(
  session: { headers: Headers },
  slug: string,
): Promise<{ id: string }> {
  const response = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: { ...Object.fromEntries(session.headers), "Content-Type": "application/json" },
      body: JSON.stringify({ name: "My Project", slug }),
    }),
  );
  return (await response.json()) as { id: string };
}

function record(overrides: Record<string, unknown> = {}) {
  return {
    schemaVersion: 2,
    runId: "run-1",
    kind: "deploy",
    promotionId: "prm-1",
    startedAt: "2025-12-31T23:58:00.000Z",
    deployedAt: "2026-01-01T00:00:00.000Z",
    trigger: { kind: "cli", actor: "victor" },
    git: { sha: "0123456789abcdef", branch: "main", dirty: false },
    trace: [
      {
        name: "build",
        app: "web",
        startedAt: "2025-12-31T23:58:00.000Z",
        finishedAt: "2025-12-31T23:59:00.000Z",
        status: "succeeded",
        log: ["next build"],
      },
    ],
    outcome: "succeeded",
    environment: { class: "production" },
    provider: { name: "aws", region: "us-east-1" },
    target: "aws/123456789012/us-east-1/main",
    apps: [
      {
        name: "web",
        runtime: { name: "nodejs22.x", arch: "arm64" },
        framework: "nextjs",
        compute: "serverless",
        urls: ["https://web.example.com"],
        hostnames: ["web.example.com"],
        outcome: "succeeded",
        variables: [{ key: "DATABASE_URL", class: "secret" }],
      },
    ],
    resources: [
      {
        name: "main",
        type: "postgres",
        binding: {
          name: "main",
          propertyKeys: ["host", "port"],
          grants: [{ verb: "read", actions: ["select"] }],
        },
      },
    ],
    usages: [{ app: "web", resource: "main", files: ["app/page.tsx"] }],
    ...overrides,
  };
}

type CreatedBody = { id: string; promotionId: string; outcome: string; deployedAt: number };
type IssueBody = { error: string; issues: { path: (string | number)[] }[] };
type ListedRow = { promotionId: string };

async function readJson<T>(response: Response): Promise<T> {
  return (await response.json()) as T;
}

function postRequest(headers: Headers, body: unknown) {
  return new Request("http://localhost/api/projects/x/deployments", {
    method: "POST",
    headers: { ...Object.fromEntries(headers), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function listRequest(headers: Headers, query = "") {
  return new Request(`http://localhost/api/projects/x/deployments${query}`, { headers });
}

describe("createDeployment", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("stores the record and answers 201", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-create");

      const response = await createDeployment(postRequest(session.headers, record()), created.id);

      expect(response.status).toBe(201);
      const body = await readJson<CreatedBody>(response);
      expect(body).toMatchObject({
        runId: "run-1",
        kind: "deploy",
        promotionId: "prm-1",
        environment: { class: "production", identity: "" },
        outcome: "succeeded",
        deployedAt: Date.parse("2026-01-01T00:00:00.000Z"),
      });
      expect(body.id).toBeTruthy();
    } finally {
      await session.cleanup();
    }
  });

  it("answers 409 when the run conflicted and was gone before it could be read back", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-vanished");

      const first = await createDeployment(postRequest(session.headers, record()), created.id);
      expect(first.status).toBe(201);

      const insert = db.insert.bind(db);
      db.insert = ((table: Parameters<typeof insert>[0]) => {
        db.insert = insert;
        return {
          values: () => ({
            onConflictDoNothing: () => ({
              returning: async () => {
                await db
                  .delete(deployment)
                  .where(and(eq(deployment.projectId, created.id), eq(deployment.runId, "run-1")));
                return [];
              },
            }),
          }),
        };
      }) as typeof db.insert;

      const second = await createDeployment(postRequest(session.headers, record()), created.id);
      expect(second.status).toBe(409);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 200 with the same id when the run is posted again", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-idempotent");

      const first = await createDeployment(postRequest(session.headers, record()), created.id);
      expect(first.status).toBe(201);
      const firstBody = await readJson<CreatedBody>(first);

      const second = await createDeployment(
        postRequest(session.headers, record({ outcome: "failed" })),
        created.id,
      );

      expect(second.status).toBe(200);
      const secondBody = await readJson<CreatedBody>(second);
      expect(secondBody.id).toBe(firstBody.id);
      expect(secondBody.outcome).toBe("succeeded");
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a record that names no target", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-no-target");

      const response = await createDeployment(
        postRequest(session.headers, record({ target: undefined })),
        created.id,
      );

      expect(response.status).toBe(400);
      expect((await readJson<IssueBody>(response)).issues[0].path).toEqual(["target"]);
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a target that fingerprints nothing past its vendor", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-bare-target");

      const response = await createDeployment(
        postRequest(session.headers, record({ target: "aws" })),
        created.id,
      );

      expect(response.status).toBe(400);
      expect((await readJson<IssueBody>(response)).issues[0].path).toEqual(["target"]);
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a succeeded deploy that names no promotion", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-no-promotion");

      const response = await createDeployment(
        postRequest(session.headers, record({ promotionId: undefined })),
        created.id,
      );

      expect(response.status).toBe(400);
      expect((await readJson<IssueBody>(response)).issues[0].path).toEqual(["promotionId"]);
    } finally {
      await session.cleanup();
    }
  });

  it("stores a destroy without a promotion", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-destroy");

      const response = await createDeployment(
        postRequest(
          session.headers,
          record({
            runId: "run-destroy",
            kind: "destroy",
            promotionId: undefined,
            apps: [],
            usages: [],
            trace: [],
          }),
        ),
        created.id,
      );

      expect(response.status).toBe(201);
      expect(await readJson<CreatedBody>(response)).toMatchObject({
        kind: "destroy",
        promotionId: null,
      });
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a stage naming an app the record does not carry", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-unknown-stage-app");

      const response = await createDeployment(
        postRequest(
          session.headers,
          record({
            trace: [
              {
                name: "build",
                app: "ghost",
                startedAt: "2025-12-31T23:58:00.000Z",
                finishedAt: "2025-12-31T23:59:00.000Z",
                status: "succeeded",
              },
            ],
          }),
        ),
        created.id,
      );

      expect(response.status).toBe(400);
      expect((await readJson<IssueBody>(response)).issues[0].path).toEqual(["trace", 0, "app"]);
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a usage naming an app the record does not carry", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-unknown-app");

      const response = await createDeployment(
        postRequest(
          session.headers,
          record({ usages: [{ app: "ghost", resource: "main", files: [] }] }),
        ),
        created.id,
      );

      expect(response.status).toBe(400);
      const body = await readJson<IssueBody>(response);
      expect(body.error).toBe("Invalid request");
      expect(body.issues).toHaveLength(1);
      expect(body.issues[0].path).toEqual(["usages", 0, "app"]);
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a usage naming a resource the record does not carry", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-unknown-resource");

      const response = await createDeployment(
        postRequest(
          session.headers,
          record({ usages: [{ app: "web", resource: "ghost", files: [] }] }),
        ),
        created.id,
      );

      expect(response.status).toBe(400);
      expect((await readJson<IssueBody>(response)).issues[0].path).toEqual([
        "usages",
        0,
        "resource",
      ]);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for a Project in an org the caller doesn't belong to", async () => {
    const session = await createTestSessionWithOrganization();
    const stranger = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-foreign");

      const response = await createDeployment(postRequest(stranger.headers, record()), created.id);

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
      await stranger.cleanup();
    }
  });

  it("answers 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-unauthed");

      const response = await createDeployment(postRequest(new Headers(), record()), created.id);

      expect(response.status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });
});

describe("listDeployments", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("lists newest first and filters by environment", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-list");

      await createDeployment(
        postRequest(
          session.headers,
          record({ runId: "run-old", promotionId: "old", deployedAt: "2026-01-01T00:00:00.000Z" }),
        ),
        created.id,
      );
      await createDeployment(
        postRequest(
          session.headers,
          record({ runId: "run-new", promotionId: "new", deployedAt: "2026-02-01T00:00:00.000Z" }),
        ),
        created.id,
      );
      await createDeployment(
        postRequest(
          session.headers,
          record({
            runId: "run-pr-7",
            kind: "preview-up",
            promotionId: "pr-7",
            deployedAt: "2026-03-01T00:00:00.000Z",
            environment: { class: "preview", identity: "pr-7" },
          }),
        ),
        created.id,
      );

      const all = await listDeployments(listRequest(session.headers), created.id);
      expect(all.status).toBe(200);
      const rows = await readJson<(ListedRow & Record<string, unknown>)[]>(all);
      expect(rows.map((row) => row.promotionId)).toEqual(["pr-7", "new", "old"]);
      expect(rows[0]).toMatchObject({
        kind: "preview-up",
        trigger: { kind: "cli", actor: "victor" },
        git: { sha: "0123456789abcdef", branch: "main", dirty: false },
        startedAt: Date.parse("2025-12-31T23:58:00.000Z"),
        environment: { class: "preview", identity: "pr-7" },
        provider: { name: "aws", region: "us-east-1" },
        target: "aws/123456789012/us-east-1/main",
        tag: null,
        edgeKind: null,
        error: null,
        apps: [{ name: "web", outcome: "succeeded", urls: ["https://web.example.com"] }],
      });

      const production = await listDeployments(
        listRequest(session.headers, "?env=production"),
        created.id,
      );
      expect((await readJson<ListedRow[]>(production)).map((row) => row.promotionId)).toEqual([
        "new",
        "old",
      ]);

      const preview = await listDeployments(
        listRequest(session.headers, "?env=preview"),
        created.id,
      );
      expect((await readJson<ListedRow[]>(preview)).map((row) => row.promotionId)).toEqual([
        "pr-7",
      ]);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for a Project in an org the caller doesn't belong to", async () => {
    const session = await createTestSessionWithOrganization();
    const stranger = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-list-foreign");

      const response = await listDeployments(listRequest(stranger.headers), created.id);

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
      await stranger.cleanup();
    }
  });

  it("answers 401 when unauthenticated", async () => {
    const response = await listDeployments(
      listRequest(new Headers()),
      "00000000-0000-7000-8000-000000000000",
    );
    expect(response.status).toBe(401);
  });
});

describe("getDeployment", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("answers the full record with trace and topology", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "deploy-get");
      const posted = await readJson<CreatedBody>(
        await createDeployment(postRequest(session.headers, record()), created.id),
      );

      const response = await getDeployment(listRequest(session.headers), created.id, posted.id);

      expect(response.status).toBe(200);
      expect(await readJson<Record<string, unknown>>(response)).toMatchObject({
        id: posted.id,
        trace: [{ name: "build", app: "web", status: "succeeded", log: ["next build"] }],
        apps: [
          { name: "web", hostnames: ["web.example.com"], variables: [{ key: "DATABASE_URL" }] },
        ],
        resources: [{ name: "main", type: "postgres" }],
        usages: [{ app: "web", resource: "main" }],
      });
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for a deployment of another project", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const mine = await createProjectFor(session, "deploy-get-mine");
      const other = await createProjectFor(session, "deploy-get-other");
      const posted = await readJson<CreatedBody>(
        await createDeployment(postRequest(session.headers, record()), other.id),
      );

      const response = await getDeployment(listRequest(session.headers), mine.id, posted.id);

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });
});
