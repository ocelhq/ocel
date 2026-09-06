export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") {
    return;
  }
  const { UnprovisionedResourceError } = await import("ocel/postgres");
  const { bootId } = await import("./lib/boot");
  const { bump } = await import("./lib/state");
  try {
    await bump(`register:${bootId()}`);
  } catch (error) {
    if (!(error instanceof UnprovisionedResourceError)) {
      throw error;
    }
  }
}
