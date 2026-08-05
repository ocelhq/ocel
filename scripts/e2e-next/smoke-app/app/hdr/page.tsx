// The header-varying probe (bd ocelhq-wvag.27, runbook §10.1). The queue leg and
// the originBlocking leg send DIFFERENT header sets to the same Lambda — the
// queue sends four literal headers plus x-forwarded-host/x-forwarded-proto, the
// fallback sends allowHeader-filtered client headers and never the x-forwarded-*
// pair — so a route whose bytes depend on the request host can regenerate to
// different bytes depending on which leg refreshed it. Without such a route the
// divergence is not falsifiable.
//
// Reading headers() directly would make this page dynamic, and a dynamic route
// has no prerender entry for the two legs to disagree about. Metadata is the way
// in: with no metadataBase, Next resolves a relative openGraph URL against the
// REQUEST's host, and the resolved absolute URL lands in the prerendered HTML.
// So the host reaches the stored bytes while the page stays a prerender.
export const revalidate = 5;

export const metadata = {
  openGraph: { url: "/hdr" },
};

export default function Page() {
  return <p id="hdr-token">hdr-token:{Date.now()}</p>;
}
