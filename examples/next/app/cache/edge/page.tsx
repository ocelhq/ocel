import { Stamp } from "../../../components/stamp";

export const runtime = "edge";
export const revalidate = 15;

export default async function CacheEdge() {
  const at = new Date().toISOString();
  return (
    <main>
      <h1>cache edge</h1>
      <Stamp scope="edge" cached={at} />
    </main>
  );
}
