import { env } from "@/infra/env";
import { resendContact, waitlistEvent } from "@/lib/waitlist";

// The console's sign-in page posts here too.
const allowedOrigins = new Set(["https://app.ocelhq.com"]);

function cors(request: Request): Record<string, string> {
  const origin = request.headers.get("origin");
  if (!origin || !allowedOrigins.has(origin)) return {};
  return {
    "access-control-allow-origin": origin,
    "access-control-allow-methods": "POST",
    "access-control-allow-headers": "content-type",
    vary: "origin",
  };
}

// PostHog is the record of who joined; Resend is who we can email. An email already on the
// list is answered with an error by Resend and is still on the list, so the visitor isn't told.
async function subscribe(email: string) {
  const resend = env.resend;
  if (!resend) {
    console.warn("waitlist: Resend is not configured, so the sign-up is in PostHog only");
    return;
  }
  const key = resend.RESEND_API_KEY;
  const segment = resend.RESEND_SEGMENT_ID;
  const res = await fetch("https://api.resend.com/contacts", {
    method: "POST",
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    body: JSON.stringify(resendContact(email, segment)),
  }).catch(() => null);
  if (!res?.ok) {
    console.error(
      "waitlist: Resend refused a contact",
      res?.status,
      await res?.text().catch(() => ""),
    );
  }
}

export function OPTIONS(request: Request) {
  return new Response(null, { status: 204, headers: cors(request) });
}

export async function POST(request: Request) {
  const headers = cors(request);
  const token = process.env.NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN;
  const host = process.env.NEXT_PUBLIC_POSTHOG_HOST;
  if (!token || !host) {
    return Response.json({ error: "The waitlist is closed right now." }, { status: 503, headers });
  }
  const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
  const event = waitlistEvent(body, token);
  if (!event) {
    return Response.json(
      { error: "That doesn't look like an email address." },
      { status: 400, headers },
    );
  }
  const [sent] = await Promise.all([
    fetch(new URL("/i/v0/e/", host), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(event),
    }).catch(() => null),
    subscribe(event.distinct_id),
  ]);
  if (!sent?.ok) {
    return Response.json({ error: "That didn't go through. Try again." }, { status: 502, headers });
  }
  return Response.json({ ok: true }, { headers });
}
