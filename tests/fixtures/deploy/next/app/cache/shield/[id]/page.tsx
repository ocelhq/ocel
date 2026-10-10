import { randomUUID } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";
import { Stamp } from "../../../../components/stamp";

export const dynamicParams = true;
export const revalidate = 3600;

const RENDER_MS = 3000;

export function generateStaticParams() {
  return [];
}

export default async function CacheShield() {
  await sleep(RENDER_MS);
  return (
    <main>
      <h1>cache shield</h1>
      <Stamp scope="shield" cached={randomUUID()} />
    </main>
  );
}
