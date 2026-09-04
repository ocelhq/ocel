import { UnprovisionedResourceError } from "ocel/postgres";

export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") {
    return;
  }
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
