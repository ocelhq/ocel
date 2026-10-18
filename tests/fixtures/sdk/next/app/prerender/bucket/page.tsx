import { uploads } from "../../../infra/index";

export const dynamic = "force-static";

export default async function PrerenderedFromABucket() {
  const { objects } = await uploads.list().page();
  const missing = await uploads.head("prerender/never-written.txt");
  return (
    <main>
      <p id="bucket">prerendered from a bucket holding {objects.length} objects</p>
      <p id="missing">and {missing === undefined ? "no" : "some"} object at a key nothing wrote</p>
      <p id="at">prerendered at {new Date().toISOString()}</p>
    </main>
  );
}
