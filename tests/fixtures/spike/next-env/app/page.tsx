import { env } from "../src/env";

export const dynamic = "force-dynamic";

export default function Page() {
  return (
    <pre id="server">
      {JSON.stringify({
        STRIPE_KEY: env.STRIPE_KEY,
        NEXT_PUBLIC_API_URL: env.NEXT_PUBLIC_API_URL,
        NEXT_PUBLIC_RETRIES: env.NEXT_PUBLIC_RETRIES,
      })}
    </pre>
  );
}
