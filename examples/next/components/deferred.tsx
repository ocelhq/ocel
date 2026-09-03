const DEFER_MS = 1500;

export async function Deferred() {
  await new Promise((resolve) => setTimeout(resolve, DEFER_MS));
  return <p data-ocel="deferred">ocel-deferred</p>;
}
