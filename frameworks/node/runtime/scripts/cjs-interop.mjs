export const cjsInterop = {
  banner: [
    'import { createRequire as ocelCreateRequire } from "node:module";',
    'import { fileURLToPath as ocelFileURLToPath } from "node:url";',
    'import { dirname as ocelDirname } from "node:path";',
    "const require = ocelCreateRequire(import.meta.url);",
    "const ocelFilename = ocelFileURLToPath(import.meta.url);",
    "const ocelDirnameOf = ocelDirname(ocelFilename);",
  ].join("\n"),
  define: { __filename: "ocelFilename", __dirname: "ocelDirnameOf" },
};
