import { createRequire } from "node:module";
import { join } from "node:path";
import { nextIncrementalCacheModule } from "@framework/next-protocol/launcher";

type NodeFileTrace = (
  files: string[],
  options: { base: string; processCwd: string },
) => Promise<{ fileList: Set<string> }>;

export async function traceRuntimeRequires(
  projectDir: string,
  repoRoot: string,
): Promise<Record<string, string>> {
  const appRequire = createRequire(join(projectDir, "package.json"));
  const { nodeFileTrace } = appRequire("next/dist/compiled/@vercel/nft") as {
    nodeFileTrace: NodeFileTrace;
  };
  const { fileList } = await nodeFileTrace([appRequire.resolve(nextIncrementalCacheModule)], {
    base: repoRoot,
    processCwd: projectDir,
  });
  return Object.fromEntries([...fileList].map((file) => [file, join(repoRoot, file)]));
}
