const ROUTE_TABLE_DIR = "route-table";
const ROUTE_TABLE_FILE = /^[0-9a-f]{64}\.json$/;
const JSON_SUFFIX = ".json";

export const ROUTE_TABLE_DELETE_MAX = 1000;

export function ownRouteTableKey(key: string, slug: string): boolean {
  const segments = key.split("/");
  return (
    segments.length === 6 &&
    segments.every((segment) => segment !== "" && segment !== "." && segment !== "..") &&
    segments[1] === slug &&
    segments[4] === ROUTE_TABLE_DIR &&
    ROUTE_TABLE_FILE.test(segments[5])
  );
}

export async function matchesRouteTableDigest(key: string, body: ArrayBuffer): Promise<boolean> {
  const file = key.slice(key.lastIndexOf("/") + 1);
  const expected = file.slice(0, -JSON_SUFFIX.length);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", body));
  const actual = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return actual === expected;
}
