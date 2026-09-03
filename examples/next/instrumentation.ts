export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") {
    return;
  }
  const { bootId } = await import("./lib/boot");
  const { bump } = await import("./lib/state");
  await bump(`register:${bootId()}`);
}
