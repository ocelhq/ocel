export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

export const variantHeadersFile = "variant-headers.json";
