import { cookies } from "next/headers";

export const dynamic = "force-dynamic";

export default async function ProxyCookies() {
  const second = (await cookies()).get("ocel-mw-2")?.value ?? "none";
  return (
    <main>
      <h1>proxy cookies</h1>
      <p data-ocel="page">mw:cookies</p>
      <p data-ocel="cookie:mw-2">{second}</p>
    </main>
  );
}
