import { spawn } from "node:child_process";

export type RunResult = {
  code: number | null;
  stdout: string;
  stderr: string;
};

export function run(
  command: string,
  args: string[],
  options: { cwd: string; env: NodeJS.ProcessEnv; timeoutMs?: number },
): Promise<RunResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timeout = options.timeoutMs
      ? setTimeout(() => child.kill("SIGTERM"), options.timeoutMs)
      : undefined;
    child.stdout.on("data", (data) => {
      stdout += data.toString();
    });
    child.stderr.on("data", (data) => {
      stderr += data.toString();
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (timeout) clearTimeout(timeout);
      resolve({ code, stdout, stderr });
    });
  });
}

export async function successful(
  label: string,
  command: string,
  args: string[],
  options: { cwd: string; env: NodeJS.ProcessEnv; timeoutMs?: number },
) {
  const result = await run(command, args, options);
  if (result.code !== 0) {
    throw new Error(
      `${label} failed with code ${result.code}\nstdout: ${result.stdout}\nstderr: ${result.stderr}`,
    );
  }
  return result;
}
