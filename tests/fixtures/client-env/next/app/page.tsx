import { createHash } from "node:crypto";
import { env } from "../infra/env";
import { BrowserGreeting } from "./browser-greeting";

export const dynamic = "force-dynamic";

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

export default function Page() {
  return (
    <main>
      <p
        data-public-value={env.NEXT_PUBLIC_GREETING}
        data-deployment-url={process.env.NEXT_PUBLIC_OCEL_URL ?? ""}
        data-sensitive-digest={sha256(env.SENSITIVE_TOKEN)}
      >
        rendered on the server
      </p>
      <BrowserGreeting />
    </main>
  );
}
