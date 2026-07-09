import { defineConfig } from "ocel/config";


// Placeholder committed so the example type-checks and reads as complete.
// `ocel init` overwrites this with the real projectId; the e2e harness deletes
// it before running init and restores it afterwards.
export default defineConfig({ projectId: "placeholder" });
