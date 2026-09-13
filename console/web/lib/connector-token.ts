import { randomUUID } from "node:crypto";
import { auth, consoleOrigin } from "@console/auth";
import type { Scope } from "@console/connectors";

export interface Reach {
  connectorId: string;
  organizationId: string;
  userId: string;
  scope: readonly Scope[];
}

const lifetime = 60;

export async function connectorToken(reach: Reach): Promise<string> {
  const issued = Math.floor(Date.now() / 1000);
  const { token } = await auth.api.signJWT({
    body: {
      payload: {
        iss: consoleOrigin(),
        aud: reach.connectorId,
        sub: `org:${reach.organizationId}`,
        act: reach.userId,
        scope: [...reach.scope],
        jti: randomUUID(),
        iat: issued,
        exp: issued + lifetime,
      },
    },
  });
  return token;
}
