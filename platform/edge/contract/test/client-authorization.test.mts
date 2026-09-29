import { expect, it } from "vitest";

import {
  CLIENT_AUTHORIZATION_HEADER,
  carryClientAuthorization,
} from "../src/client-authorization.mjs";

it("carries the client's authorization in the carrier header", () => {
  const headers = new Headers({ authorization: "Bearer client-token" });

  carryClientAuthorization(headers);

  expect(headers.get(CLIENT_AUTHORIZATION_HEADER)).toBe("Bearer client-token");
  expect(headers.get("authorization")).toBe("Bearer client-token");
});

it("drops a carrier the client sent, so a client never names the authorization the origin restores", () => {
  const forged = new Headers({ [CLIENT_AUTHORIZATION_HEADER]: "Bearer forged" });

  carryClientAuthorization(forged);

  expect(forged.has(CLIENT_AUTHORIZATION_HEADER)).toBe(false);

  const both = new Headers({
    authorization: "Bearer client-token",
    [CLIENT_AUTHORIZATION_HEADER]: "Bearer forged",
  });

  carryClientAuthorization(both);

  expect(both.get(CLIENT_AUTHORIZATION_HEADER)).toBe("Bearer client-token");
});
