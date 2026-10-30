import posthog from "posthog-js";

export function capture(event: string, properties?: Record<string, string>) {
  if (posthog.__loaded) posthog.capture(event, properties);
}
