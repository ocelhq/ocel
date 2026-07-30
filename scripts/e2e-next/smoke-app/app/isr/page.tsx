// The smoke job's revalidation probe. It is a node-parented prerender with a
// short revalidate window whose body changes on every render, so a request made
// after the window can prove the *stored cache entry was rewritten* — the one
// failure mode "do all routes return 200?" can never see: if the worker's
// `revalidates` flag were wrong, this page would keep serving its very first
// token forever, at 200.
//
// Keep the marker and `revalidate` in sync with ISR_ROUTE / ISR_REVALIDATE_SECONDS
// in ../../lib.mjs.
export const revalidate = 5;

export default function Page() {
  return <p id="isr-token">isr-token:{Date.now()}</p>;
}
