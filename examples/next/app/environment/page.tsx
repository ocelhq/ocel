"use client";

import { clientEnv } from "ocel/env/client";

export default function EnvironmentPage() {
  return (
    <main>
      <p id="app-id">{clientEnv.NEXT_PUBLIC_APP_ID}</p>
      <p id="analytics-id">{clientEnv.NEXT_PUBLIC_GA4_ID}</p>
    </main>
  );
}
