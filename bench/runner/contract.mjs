export const DRIVER_EXPORTS = Object.freeze(["deploy", "teardown"]);

export const DEPLOYMENT_FIELDS = Object.freeze(["url", "functionName", "buildMs", "provisionMs"]);

export function driverProblems(id, driver) {
  const problems = DRIVER_EXPORTS.filter((name) => typeof driver?.[name] !== "function").map(
    (name) => `${id} exports no ${name}()`,
  );
  return problems;
}

export function deploymentProblems(id, deployment) {
  if (!deployment || typeof deployment !== "object") {
    return [`${id} deploy() resolved ${JSON.stringify(deployment)}, not a deployment`];
  }
  const problems = [];
  if (typeof deployment.url !== "string" || !/^https?:\/\//.test(deployment.url)) {
    problems.push(`${id} deploy() returned url ${JSON.stringify(deployment.url)}, not an http(s) URL`);
  }
  if (typeof deployment.functionName !== "string" || !deployment.functionName) {
    problems.push(
      `${id} deploy() returned functionName ${JSON.stringify(deployment.functionName)}; ` +
        `without it no cold start can be forced and no REPORT line can be read`,
    );
  }
  for (const field of ["buildMs", "provisionMs"]) {
    if (!Number.isFinite(deployment[field])) {
      problems.push(`${id} deploy() returned ${field} ${JSON.stringify(deployment[field])}, not a number`);
    }
  }
  return problems;
}

export function cellId(app, platform) {
  return `${app}/${platform}`;
}

export function expandMatrix({ apps, platforms, only }) {
  const wantApp = filter(only?.frameworks);
  const wantPlatform = filter(only?.platforms);
  return apps
    .filter((app) => wantApp(app.name))
    .flatMap((app) =>
      platforms.filter((platform) => wantPlatform(platform.id)).map((platform) => ({
        id: cellId(app.name, platform.id),
        app,
        platform,
      })),
    );
}

function filter(names) {
  if (!names || names.length === 0) return () => true;
  const wanted = new Set(names.map((name) => String(name).trim()).filter(Boolean));
  return (name) => wanted.has(name);
}
