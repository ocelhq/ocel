import { stripVTControlCharacters } from "node:util";

export const ENGINE_ENV = "OCEL_VPS_DOCKER";

export function engineSeries(env: NodeJS.ProcessEnv): string | undefined {
  const series = env[ENGINE_ENV]?.trim();
  if (!series) {
    return undefined;
  }
  if (!/^\d+\.\d+$/.test(series)) {
    throw new Error(
      `${ENGINE_ENV}=${series} names no docker release series; name one as <major>.<minor>, such as 28.0`,
    );
  }
  return series;
}

export function adoptionMissed(said: string, series: string): string | undefined {
  const plain = stripVTControlCharacters(said);
  const row = new RegExp(
    `adopt docker\\s+docker:engine\\s+— docker ${series.replaceAll(".", "\\.")}\\.\\d+, not managed by ocel: upgrading it is yours`,
  );
  if (row.test(plain)) {
    return undefined;
  }
  const shown = plain
    .split("\n")
    .filter((line) => line.includes("docker:engine"))
    .map((line) => line.trim());
  return (
    `the bootstrap plan shows no adopt row for the docker ${series}.x the box was given` +
    (shown.length > 0
      ? `; its engine row reads:\n  ${shown.join("\n  ")}`
      : ", and no engine row at all")
  );
}
