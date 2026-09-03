import { spawn } from "node:child_process";
import { redact } from "../../contract";

export type Ran = { code: number | null; stdout: string; stderr: string };

export async function spawnBin(
  bin: string,
  args: string[],
  cwd: string,
  env: NodeJS.ProcessEnv,
): Promise<Ran> {
  const result = await new Promise<Ran>((resolve, reject) => {
    const child = spawn(bin, args, { cwd, env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
  if (result.code !== 0) {
    throw new Error(
      `${bin} ${redact(args.join(" "))} exited ${result.code}\nstdout: ${redact(result.stdout)}\nstderr: ${redact(result.stderr)}`,
    );
  }
  return result;
}
