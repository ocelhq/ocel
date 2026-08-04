// The tag-publisher's probe: the one route that makes the origin raise a tag
// invalidation on demand.
//
// A raise is a durable write to the state table's TAG# partition, and that write
// IS the invalidation (epic decision 1). Everything downstream of it — the
// DynamoDB stream, the account-level publisher, the S3 copy of the build's tag
// clock and the R2 copy the edge reads — is machinery no request ever touches
// and no 200 can prove. assert-tag-publisher.mjs drives this route and then
// looks for the record in both stores.
//
// It deliberately does not need anything to be cached under the tag. The
// handler's revalidateTag runs whether or not the tag names a live entry, so the
// probe tests the publishing path rather than a particular page's caching.
//
// Keep the route and the tag prefix in sync with TAG_PROBE_ROUTE / tagProbeTag
// in ../../../lib.mjs.
// In Next 16.2.10 `revalidateTag` takes the cache-life profile as a second,
// required argument: `revalidateTag(tag: string, profile: string |
// CacheLifeConfig)`. The one-argument form is `updateTag`, which is callable
// only from a Server Action, so a route handler must pass a profile.
import { revalidateTag } from "next/cache";

export const dynamic = "force-dynamic";

export async function POST(request: Request): Promise<Response> {
  const tag = new URL(request.url).searchParams.get("tag");
  if (!tag) {
    return Response.json({ error: "no tag given" }, { status: 400 });
  }
  revalidateTag(tag, "default");
  return Response.json({ revalidated: tag, at: Date.now() });
}
