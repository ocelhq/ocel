import { files } from "../infra/files";

export const dynamic = "force-static";

export default async function Home() {
  const { objects } = await files.list().page();
  const missing = await files.head("prerender/never-written.txt");
  return (
    <main>
      <p id="bucket">prerendered from a bucket holding {objects.length} objects</p>
      <p id="missing">and {missing === null ? "no" : "some"} object at a key nothing wrote</p>
      <p id="at">prerendered at {new Date().toISOString()}</p>
    </main>
  );
}
