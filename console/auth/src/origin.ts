export function consoleOrigin(): string {
  const named = process.env.BETTER_AUTH_URL;
  if (!named) {
    throw new Error("BETTER_AUTH_URL is unset, so nothing names this console to a connector");
  }
  return new URL(named).origin;
}
