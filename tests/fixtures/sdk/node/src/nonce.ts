import { timingSafeEqual } from "node:crypto";
import { env } from "../infra/variables";

export function isJourneyNonce(given: string | undefined): boolean {
  if (given === undefined) {
    return false;
  }
  const [givenBytes, expectedBytes] = [Buffer.from(given), Buffer.from(env.JOURNEY_NONCE)];
  return (
    givenBytes.byteLength === expectedBytes.byteLength && timingSafeEqual(givenBytes, expectedBytes)
  );
}
