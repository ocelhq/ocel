import { readFileSync } from "node:fs";
import { join } from "node:path";

export function readDocsOrigin(root) {
  const source = readFileSync(join(root, "cli", "internal", "docsurl", "docsurl.go"), "utf8");
  const origin = /^const Origin = "([^"]+)"$/m.exec(source)?.[1];
  if (!origin) {
    throw new Error("cli/internal/docsurl/docsurl.go declares no Origin constant");
  }
  return origin;
}
