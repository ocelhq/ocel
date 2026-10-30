const emailShape = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function waitlistEvent(
  body: { email?: unknown; source?: unknown; company?: unknown },
  token: string,
) {
  const email = typeof body.email === "string" ? body.email.trim().toLowerCase() : "";
  if (!emailShape.test(email) || email.length > 254 || body.company) {
    return null;
  }
  const source = typeof body.source === "string" ? body.source.slice(0, 40) : "unknown";
  return {
    api_key: token,
    event: "waitlist_joined",
    distinct_id: email,
    properties: {
      source,
      $set: { email },
      $set_once: { waitlist_source: source },
    },
  };
}

export function resendContact(email: string, segmentId: string) {
  return { email, unsubscribed: false, segments: [{ id: segmentId }] };
}
