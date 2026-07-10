import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../test/auth-harness";
import { setupTestDatabase } from "../../../test/db";
import { createProject } from "../projects/route";
import { presignUpload } from "./presign/route";
import { detectUploads, expireOverdueSessions } from "./detect/route";
import type { SessionFile } from "./session";

const encodedMetadata = Buffer.from(
  JSON.stringify({ uploader: "avatar", metadata: {} }),
).toString("base64");

async function createProjectFor(session: { headers: Headers }, slug: string) {
  const res = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "P", slug }),
    }),
  );
  return res.json() as Promise<{ id: string }>;
}

async function presign(
  session: { headers: Headers },
  projectId: string,
): Promise<string> {
  const res = await presignUpload(
    new Request("http://localhost/api/blob/presign", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        projectId,
        bucket: "storage",
        files: [
          { key: "a.png", name: "a.png", size: 3, mimeType: "image/png" },
          { key: "b.png", name: "b.png", size: 3, mimeType: "image/png" },
        ],
        metadata: encodedMetadata,
        callbackBaseUrl: "http://localhost:3000/api/upload",
      }),
    }),
  );
  const { sessionId } = await res.json();
  return sessionId;
}

function detectRequest(projectId: string, headers: Headers) {
  return new Request("http://localhost/api/blob/detect", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ projectId }),
  });
}

async function files(sessionId: string): Promise<SessionFile[]> {
  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));
  return row.files as SessionFile[];
}

async function setFileStates(sessionId: string, states: SessionFile["state"][]) {
  const next = (await files(sessionId)).map((f, i) => ({
    ...f,
    state: states[i],
  }));
  await db
    .update(uploadSession)
    .set({ files: next })
    .where(eq(uploadSession.id, sessionId));
}

async function makeOverdue(sessionId: string) {
  await db
    .update(uploadSession)
    .set({ expiresAt: new Date(Date.now() - 60_000) })
    .where(eq(uploadSession.id, sessionId));
}

// Pure-DB (no MinIO): the expiry sweep is a jsonb state transition. detect skips
// the store HEAD for any non-pending file, so an all-succeeded session never
// touches MinIO either - the idempotency assertions run without a store.
describe("blob expiry sweep (via POST /api/blob/detect)", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("transitions still-pending files of an overdue session to expired", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "expiry-basic");
      const sessionId = await presign(session, projectId);
      await makeOverdue(sessionId);

      const res = await detectUploads(detectRequest(projectId, session.headers));
      // Expiry is a non-completion: no callback is emitted for expired files.
      expect((await res.json()).completions).toHaveLength(0);

      const after = await files(sessionId);
      expect(after.map((f) => f.state)).toEqual(["expired", "expired"]);
    } finally {
      await session.cleanup();
    }
  });

  it("never downgrades a succeeded file past TTL to expired", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "expiry-succeeded");
      const sessionId = await presign(session, projectId);
      // One file already landed and was completed before the session aged out.
      await setFileStates(sessionId, ["succeeded", "pending"]);
      await makeOverdue(sessionId);

      await detectUploads(detectRequest(projectId, session.headers));

      const after = await files(sessionId);
      expect(after.map((f) => f.state)).toEqual(["succeeded", "expired"]);
    } finally {
      await session.cleanup();
    }
  });

  it("is idempotent across repeated sweeps", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "expiry-idempotent");
      const sessionId = await presign(session, projectId);
      await setFileStates(sessionId, ["succeeded", "pending"]);
      await makeOverdue(sessionId);

      await detectUploads(detectRequest(projectId, session.headers));
      const firstPass = (await files(sessionId)).map((f) => f.state);
      await detectUploads(detectRequest(projectId, session.headers));
      await detectUploads(detectRequest(projectId, session.headers));
      const finalPass = (await files(sessionId)).map((f) => f.state);

      expect(firstPass).toEqual(["succeeded", "expired"]);
      expect(finalPass).toEqual(["succeeded", "expired"]);
    } finally {
      await session.cleanup();
    }
  });

  it("leaves a still-live session's pending files untouched (TTL guard)", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "expiry-live");
      const sessionId = await presign(session, projectId);
      // Do NOT makeOverdue: presign set ~2h expiry. Exercise the sweep in
      // isolation (not full detect, which would HEAD the store for live pending
      // files) so the `expires_at <= now()` guard is what's under test.
      await expireOverdueSessions(projectId, session.user.id);

      const after = await files(sessionId);
      expect(after.map((f) => f.state)).toEqual(["pending", "pending"]);
    } finally {
      await session.cleanup();
    }
  });

  it("emits no completion for an already-succeeded session on repeated detects (exactly-once)", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "expiry-once");
      const sessionId = await presign(session, projectId);
      // Simulate a session whose files already succeeded (completion already
      // fired). Further detect ticks must never re-emit a completion.
      await setFileStates(sessionId, ["succeeded", "succeeded"]);

      const a = await detectUploads(detectRequest(projectId, session.headers));
      const b = await detectUploads(detectRequest(projectId, session.headers));
      expect((await a.json()).completions).toHaveLength(0);
      expect((await b.json()).completions).toHaveLength(0);
      expect((await files(sessionId)).map((f) => f.state)).toEqual([
        "succeeded",
        "succeeded",
      ]);
    } finally {
      await session.cleanup();
    }
  });
});
