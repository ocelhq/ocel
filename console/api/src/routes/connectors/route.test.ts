import { db } from "@console/db";
import { connector } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import {
  createTestSessionWithOrganization,
  createTestSessionWithRole,
} from "../../../test/auth-harness";
import { setupTestDatabase } from "../../../test/db";
import { deleteConnector, updateConnector } from "./[id]/route";
import { listConnectors, upsertConnector } from "./route";

const vpsTarget = "vps/sha256:abc/ocel";

function putRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/connectors", {
    method: "PUT",
    headers: { ...Object.fromEntries(headers), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function patchRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/connectors/x", {
    method: "PATCH",
    headers: { ...Object.fromEntries(headers), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function getRequest(headers: Headers) {
  return new Request("http://localhost/api/connectors", { headers });
}

function deleteRequest(headers: Headers) {
  return new Request("http://localhost/api/connectors/x", { method: "DELETE", headers });
}

const dialled = { target: vpsTarget, vendor: "vps", reach: "dial" };

describe("PUT /api/connectors", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("creates the row in the caller's active org", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await upsertConnector(putRequest(dialled, session.headers));

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body.target).toBe(vpsTarget);
      expect(body.vendor).toBe("vps");
      expect(body.reach).toBe("dial");
      expect(body.compute).toBeNull();
      expect(body.url).toBeNull();
      expect(body.organizationId).toBe(session.organization.id);
      expect(body.id).toBeTruthy();
    } finally {
      await session.cleanup();
    }
  });

  it("converges on the same row for the same target and keeps the url and key", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const first = await (await upsertConnector(putRequest(dialled, session.headers))).json();
      await updateConnector(
        patchRequest(
          { url: "https://box.example/.ocel/connector", publicKey: "aGk=" },
          session.headers,
        ),
        first.id,
      );

      const again = await upsertConnector(putRequest(dialled, session.headers));
      expect(again.status).toBe(200);
      const body = await again.json();
      expect(body.id).toBe(first.id);
      expect(body.url).toBe("https://box.example/.ocel/connector");
      expect(body.publicKey).toBe("aGk=");

      const rows = await db
        .select()
        .from(connector)
        .where(eq(connector.organizationId, session.organization.id));
      expect(rows).toHaveLength(1);
    } finally {
      await session.cleanup();
    }
  });

  it("lets two orgs hold the same target", async () => {
    const one = await createTestSessionWithOrganization();
    const two = await createTestSessionWithOrganization();

    try {
      expect((await upsertConnector(putRequest(dialled, one.headers))).status).toBe(200);
      expect((await upsertConnector(putRequest(dialled, two.headers))).status).toBe(200);
    } finally {
      await one.cleanup();
      await two.cleanup();
    }
  });

  it("refuses a target that names no account", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await upsertConnector(
        putRequest({ ...dialled, target: "vps" }, session.headers),
      );
      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("takes a vendor no table in the console names", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await upsertConnector(
        putRequest({ target: "azure/sub-1/westeurope/ocel", vendor: "azure" }, session.headers),
      );
      expect(response.status).toBe(200);
      expect((await response.json()).vendor).toBe("azure");
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a vendor outside the segment charset and a reach outside the enum", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      for (const vendor of ["AWS", "aws/two", "aws two", ""]) {
        expect(
          (await upsertConnector(putRequest({ ...dialled, vendor }, session.headers))).status,
        ).toBe(400);
      }
      expect(
        (
          await upsertConnector(
            putRequest({ ...dialled, target: "vps/SHA256:abc/ocel" }, session.headers),
          )
        ).status,
      ).toBe(400);
      expect(
        (await upsertConnector(putRequest({ ...dialled, reach: "poll" }, session.headers))).status,
      ).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    expect((await upsertConnector(putRequest(dialled, new Headers()))).status).toBe(401);
  });

  it("refuses a plain member with 403", async () => {
    const session = await createTestSessionWithOrganization();
    const plain = await createTestSessionWithRole(session.organization.id, "member");

    try {
      const response = await upsertConnector(putRequest(dialled, plain.headers));
      expect(response.status).toBe(403);
      expect(
        await db
          .select()
          .from(connector)
          .where(eq(connector.organizationId, session.organization.id)),
      ).toHaveLength(0);
    } finally {
      await plain.cleanup();
      await session.cleanup();
    }
  });

  it("lets an admin hold a target", async () => {
    const session = await createTestSessionWithOrganization();
    const deputy = await createTestSessionWithRole(session.organization.id, "admin");

    try {
      expect((await upsertConnector(putRequest(dialled, deputy.headers))).status).toBe(200);
    } finally {
      await deputy.cleanup();
      await session.cleanup();
    }
  });
});

describe("GET /api/connectors", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("lists the active org's rows and none from another", async () => {
    const session = await createTestSessionWithOrganization();
    const other = await createTestSessionWithOrganization();

    try {
      await upsertConnector(putRequest(dialled, session.headers));
      await upsertConnector(
        putRequest({ ...dialled, target: "aws/1/eu-west-1/ocel", vendor: "aws" }, other.headers),
      );

      const response = await listConnectors(getRequest(session.headers));
      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body).toHaveLength(1);
      expect(body[0].target).toBe(vpsTarget);
    } finally {
      await session.cleanup();
      await other.cleanup();
    }
  });

  it("says whether each row is online, so nothing downstream recomputes the threshold", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();

      const never = await (await listConnectors(getRequest(session.headers))).json();
      expect(never[0].online).toBe(false);

      const now = new Date();
      await db
        .update(connector)
        .set({ connectedAt: now, lastSeenAt: now })
        .where(eq(connector.id, held.id));
      const fresh = await (await listConnectors(getRequest(session.headers))).json();
      expect(fresh[0].online).toBe(true);

      await db
        .update(connector)
        .set({ lastSeenAt: new Date(Date.now() - 600_000) })
        .where(eq(connector.id, held.id));
      const stale = await (await listConnectors(getRequest(session.headers))).json();
      expect(stale[0].online).toBe(false);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    expect((await listConnectors(getRequest(new Headers()))).status).toBe(401);
  });

  it("lists for a plain member", async () => {
    const session = await createTestSessionWithOrganization();
    const plain = await createTestSessionWithRole(session.organization.id, "member");

    try {
      await upsertConnector(putRequest(dialled, session.headers));

      const response = await listConnectors(getRequest(plain.headers));
      expect(response.status).toBe(200);
      expect(await response.json()).toHaveLength(1);
    } finally {
      await plain.cleanup();
      await session.cleanup();
    }
  });
});

describe("PATCH and DELETE /api/connectors/{id}", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("writes back the url, the public key, the tls pin and the compute the provider chose", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();
      const response = await updateConnector(
        patchRequest(
          {
            url: "https://box.example/.ocel/connector",
            publicKey: "aGk=",
            tlsPin: "sha256/xyz",
            compute: "serverless",
          },
          session.headers,
        ),
        held.id,
      );

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body.url).toBe("https://box.example/.ocel/connector");
      expect(body.publicKey).toBe("aGk=");
      expect(body.tlsPin).toBe("sha256/xyz");
      expect(body.compute).toBe("serverless");
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a body naming nothing and a url that is not one", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();
      expect((await updateConnector(patchRequest({}, session.headers), held.id)).status).toBe(400);
      expect(
        (await updateConnector(patchRequest({ url: "box" }, session.headers), held.id)).status,
      ).toBe(400);
      expect(
        (await updateConnector(patchRequest({ compute: "vm" }, session.headers), held.id)).status,
      ).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("deletes the row and answers 204", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();
      const response = await deleteConnector(deleteRequest(session.headers), held.id);

      expect(response.status).toBe(204);
      const rows = await db
        .select()
        .from(connector)
        .where(and(eq(connector.id, held.id)));
      expect(rows).toHaveLength(0);
    } finally {
      await session.cleanup();
    }
  });

  it("hides another org's row behind a 404", async () => {
    const session = await createTestSessionWithOrganization();
    const other = await createTestSessionWithOrganization();

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();

      expect(
        (await updateConnector(patchRequest({ url: "https://x.test/c" }, other.headers), held.id))
          .status,
      ).toBe(404);
      expect((await deleteConnector(deleteRequest(other.headers), held.id)).status).toBe(404);
    } finally {
      await session.cleanup();
      await other.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    expect(
      (await updateConnector(patchRequest({ url: "https://x.test/c" }, new Headers()), "x")).status,
    ).toBe(401);
  });

  it("refuses a plain member with 403 and leaves the url alone", async () => {
    const session = await createTestSessionWithOrganization();
    const plain = await createTestSessionWithRole(session.organization.id, "member");

    try {
      const held = await (await upsertConnector(putRequest(dialled, session.headers))).json();

      expect(
        (
          await updateConnector(
            patchRequest({ url: "https://attacker.example/c" }, plain.headers),
            held.id,
          )
        ).status,
      ).toBe(403);
      expect((await deleteConnector(deleteRequest(plain.headers), held.id)).status).toBe(403);

      const [row] = await db.select().from(connector).where(eq(connector.id, held.id));
      expect(row.url).toBeNull();
    } finally {
      await plain.cleanup();
      await session.cleanup();
    }
  });
});
