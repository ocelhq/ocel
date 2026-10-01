import {
  createPrivateKey,
  generateKeyPairSync,
  type KeyObject,
  sign,
  timingSafeEqual,
} from "node:crypto";
import { readFileSync } from "node:fs";
import express from "express";
import { RealtimePublishError } from "ocel/realtime";
import { createRealtimeHandler } from "ocel/realtime/express";
import { rt } from "../infra/index";
import { env } from "../infra/variables";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3113);
const BINDING_KEY = `OCEL_RESOURCE_REALTIME_${rt.name}`;
const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");

type Signer = "binding" | "another-key" | "nobody";

const app = express();

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.all("/api/realtime", createRealtimeHandler(rt));

app.post("/api/publish", express.json({ limit: "1mb" }), async (req, res) => {
  const { pattern, params, body } = req.body;
  try {
    await rt.publish(pattern, { params, body });
    res.status(204).end();
  } catch (error) {
    if (error instanceof RealtimePublishError) {
      res.status(422).json({ code: error.code });
      return;
    }
    res.status(502).json({ error: (error as Error).message });
  }
});

function isJourneyNonce(given: string | undefined): boolean {
  if (given === undefined) {
    return false;
  }
  const [givenBytes, expectedBytes] = [Buffer.from(given), Buffer.from(env.JOURNEY_NONCE)];
  return (
    givenBytes.byteLength === expectedBytes.byteLength && timingSafeEqual(givenBytes, expectedBytes)
  );
}

function readBinding(): string {
  const dir = process.env.OCEL_LIVE_DIR;
  if (dir) {
    try {
      return readFileSync(`${dir}/${BINDING_KEY}`, "utf8");
    } catch {}
  }
  const raw = process.env[BINDING_KEY];
  if (!raw) {
    throw new Error(`${BINDING_KEY} was not delivered`);
  }
  return raw;
}

function readSigningKey(signer: Signer): KeyObject | undefined {
  switch (signer) {
    case "binding": {
      const seed = Buffer.from(JSON.parse(readBinding()).realtime.signingKey, "base64");
      return createPrivateKey({
        key: Buffer.concat([PKCS8_ED25519_PREFIX, seed]),
        format: "der",
        type: "pkcs8",
      });
    }
    case "another-key":
      return generateKeyPairSync("ed25519").privateKey;
    case "nobody":
      return undefined;
  }
}

function encodeSegment(value: unknown): string {
  return Buffer.from(JSON.stringify(value)).toString("base64url");
}

app.post("/api/tokens", express.json(), (req, res) => {
  if (!isJourneyNonce(req.get("x-journey-nonce"))) {
    res.status(403).json({ error: "signing a token needs the nonce the harness set" });
    return;
  }
  const { header, claims, signedBy } = req.body as {
    header: unknown;
    claims: unknown;
    signedBy: Signer;
  };
  const input = `${encodeSegment(header)}.${encodeSegment(claims)}`;
  const key = readSigningKey(signedBy);
  const signature = key ? sign(null, Buffer.from(input), key).toString("base64url") : "";
  res.json({ token: `${input}.${signature}` });
});

app.listen(PORT, () => {
  console.log(`realtime fixture listening on http://localhost:${PORT}`);
});
