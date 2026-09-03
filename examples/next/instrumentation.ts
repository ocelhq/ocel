export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") {
    return;
  }
  const { bump } = await import("./lib/state");
  await bump("register");
}
