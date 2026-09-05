import { Stamp } from "../../../components/stamp";

export const dynamic = "force-static";
export const revalidate = 3600;

export default async function CachePath() {
  const at = new Date().toISOString();
  return (
    <main>
      <h1>cache path</h1>
      <Stamp scope="path" cached={at} />
    </main>
  );
}
