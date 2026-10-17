import { join } from "node:path";
import { readDocsOrigin } from "./docs-origin.mjs";

export function readPublishedSchemas(root) {
  const origin = readDocsOrigin(root);
  const published = join(root, "www", "public", "schema");
  return {
    config: {
      path: join(published, "ocel.schema.json"),
      url: `${origin}/schema/ocel.schema.json`,
    },
    messages: {
      dir: join(published, "cli"),
      url: `${origin}/schema/cli`,
    },
  };
}
