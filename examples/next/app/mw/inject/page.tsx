import { headers } from "next/headers";

export const dynamic = "force-dynamic";

export default async function ProxyInject() {
  const injected = (await headers()).get("x-ocel-injected") ?? "none";
  return (
    <main>
      <h1>proxy inject</h1>
      <p data-ocel="page">mw:inject</p>
      <p data-ocel="header:injected">{injected}</p>
    </main>
  );
}
