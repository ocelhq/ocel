export const DESTROY_PAGES = 25;

export async function emptyPrefix(
  bucket: R2Bucket,
  isrPrefix: string,
): Promise<"emptied" | "more"> {
  let cursor: string | undefined;
  for (let page = 0; page < DESTROY_PAGES; page++) {
    const listing = await bucket.list({ prefix: `${isrPrefix}/`, cursor });
    if (listing.objects.length > 0) {
      await bucket.delete(listing.objects.map((object) => object.key));
    }
    if (!listing.truncated) return "emptied";
    cursor = listing.cursor;
  }
  return "more";
}
