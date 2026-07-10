import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../../projects/route";
import { presignUpload } from "../presign/route";
import { signUpload } from "../signing";
import { verifyUploadSignature } from "./route";

const encodedMetadata = Buffer.from(
  JSON.stringify({ uploader: "avatar", metadata: { userId: "u1" } }),
).toString("base64");

async function seedSession(
  session: { headers: Headers },
  slug: string,
): Promise<{ sessionId: string; secret: string; key: string }> {
  const created = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "P", slug }),
    }),
  );
  const { id: projectId } = await created.json();

  const presignRes = await presignUpload(
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
        ],
        metadata: encodedMetadata,
        callbackBaseUrl: "http://localhost:3000/api/upload",
      }),
    }),
  );
  const { sessionId, files } = await presignRes.json();
  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));
  return { sessionId, secret: row.secret, key: files[0].key };
}

function verifyRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/blob/verify", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

describe("POST /api/blob/verify", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("accepts a correctly-signed callback and returns the stored metadata verbatim", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { sessionId, secret, key } = await seedSession(session, "verify-ok");
      const file = { key, name: "a.png", size: 3, mimeType: "image/png" };
      const signature = signUpload(secret, sessionId, file);

      const res = await verifyUploadSignature(
        verifyRequest({ sessionId, signature, file }, session.headers),
      );
      expect(res.status).toBe(200);
      const body = await res.json();
      expect(body.valid).toBe(true);
      expect(body.metadata).toBe(encodedMetadata);
    } finally {
      await session.cleanup();
    }
  });

  it("rejects a forged signature", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { sessionId, key } = await seedSession(session, "verify-forged");
      const file = { key, name: "a.png", size: 3, mimeType: "image/png" };

      const res = await verifyUploadSignature(
        verifyRequest(
          { sessionId, signature: "deadbeef", file },
          session.headers,
        ),
      );
      expect(res.status).toBe(200);
      const body = await res.json();
      expect(body.valid).toBe(false);
      expect(body.metadata).toBeUndefined();
    } finally {
      await session.cleanup();
    }
  });

  it("rejects a signature over a tampered file (different key)", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { sessionId, secret, key } = await seedSession(
        session,
        "verify-tampered",
      );
      const file = { key, name: "a.png", size: 3, mimeType: "image/png" };
      const signature = signUpload(secret, sessionId, file);

      const res = await verifyUploadSignature(
        verifyRequest(
          { sessionId, signature, file: { ...file, key: `${key}.evil` } },
          session.headers,
        ),
      );
      const body = await res.json();
      expect(body.valid).toBe(false);
    } finally {
      await session.cleanup();
    }
  });

  it("returns valid:false for a session in an org the caller doesn't belong to", async () => {
    const owner = await createTestSessionWithOrganization();
    const other = await createTestSessionWithOrganization();
    try {
      const { sessionId, secret, key } = await seedSession(owner, "verify-cross-tenant");
      const file = { key, name: "a.png", size: 3, mimeType: "image/png" };
      // Even a correctly-signed callback is refused for a non-member caller.
      const signature = signUpload(secret, sessionId, file);
      const res = await verifyUploadSignature(
        verifyRequest({ sessionId, signature, file }, other.headers),
      );
      expect((await res.json()).valid).toBe(false);
    } finally {
      await owner.cleanup();
      await other.cleanup();
    }
  });

  it("returns valid:false for an unknown session", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const res = await verifyUploadSignature(
        verifyRequest(
          {
            sessionId: "nope",
            signature: "x",
            file: { key: "k", name: "n", size: 1, mimeType: "image/png" },
          },
          session.headers,
        ),
      );
      const body = await res.json();
      expect(body.valid).toBe(false);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const res = await verifyUploadSignature(
      verifyRequest(
        {
          sessionId: "s",
          signature: "x",
          file: { key: "k", name: "n", size: 1, mimeType: "image/png" },
        },
        new Headers(),
      ),
    );
    expect(res.status).toBe(401);
  });
});
