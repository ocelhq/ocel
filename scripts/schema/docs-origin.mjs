import { readFileSync } from "node:fs";
import { join } from "node:path";

const initSource = join("cli", "internal", "commands", "projectinit", "init.go");
const schemaPath = "/schema/ocel.schema.json";

export function readDocsOrigin(root) {
  const source = readFileSync(join(root, initSource), "utf8");
  const schema = /^const configSchema = "([^"]+)"$/m.exec(source)?.[1];
  if (!schema?.endsWith(schemaPath)) {
    throw new Error(`${initSource} declares no configSchema constant ending in ${schemaPath}`);
  }
  return schema.slice(0, -schemaPath.length);
}
