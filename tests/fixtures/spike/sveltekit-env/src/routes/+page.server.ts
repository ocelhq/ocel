import { BUILD_TAG, PORT_HINT, STRIPE_KEY } from "$app/env/private";
import { PUBLIC_API_URL, PUBLIC_RETRIES } from "$app/env/public";

export function load() {
  return {
    server: { STRIPE_KEY, BUILD_TAG, PORT_HINT, PUBLIC_API_URL, PUBLIC_RETRIES },
  };
}
