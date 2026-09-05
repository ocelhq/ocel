"use server";

import { bump } from "../../lib/state";

export async function record(formData: FormData) {
  await bump(`action:${String(formData.get("note") ?? "")}`);
}
