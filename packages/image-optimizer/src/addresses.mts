import ipaddr from "ipaddr.js";

// The IP policy, and the only one: an address is reachable if and only if
// ipaddr.js calls its range "unicast".
//
// It is written as an allowlist on purpose. A blocklist of CIDRs has to
// enumerate RFC1918, loopback, link-local (including 169.254.169.254, the
// instance metadata service that turns an SSRF into this function's IAM role),
// CGNAT 100.64/10, 0.0.0.0/8, the TEST-NETs, multicast, every reserved block,
// ULA fc00::/7, fe80::/10, NAT64, 6to4 and Teredo — and then stay correct as
// IANA adds more. The `ip` npm package tried and earned CVE-2023-42282 and
// CVE-2024-29415 for the attempt. "unicast" is what is left after ipaddr.js
// removes all of them, and a block IANA allocates tomorrow is denied by default
// rather than allowed until someone notices.
//
// The address handed here must always come from a resolver or from a hostname
// that `new URL()` has already normalized. That normalization is what kills
// 0x7f000001, 2130706433 and 0177.0.0.1: the URL parser turns all three into
// 127.0.0.1 before this function ever sees them, and this function would
// otherwise reject them merely as unparseable rather than as loopback.
export function isReachableAddress(address: string): boolean {
  // A resolver never emits brackets, but a URL hostname for an IPv6 literal
  // always does, and both call this.
  const bare =
    address.startsWith("[") && address.endsWith("]")
      ? address.slice(1, -1)
      : address;

  // Default deny: anything that is not a valid address is not something we are
  // willing to open a socket to. This is the one place this function's answer
  // differs from Next's isPrivateIp, which reports a hostname as "not private"
  // because it answers a different question — it is asked about names, and this
  // is only ever asked about resolved addresses.
  if (!ipaddr.isValid(bare)) return false;

  try {
    let parsed = ipaddr.parse(bare);
    // ::ffff:127.0.0.1 is loopback wearing an IPv6 costume; ipaddr.js ranges it
    // as "ipv4Mapped" rather than as "loopback", so it has to be unwrapped
    // before the range is read or the costume works.
    if (parsed.kind() === "ipv6") {
      const v6 = parsed as ipaddr.IPv6;
      if (v6.isIPv4MappedAddress()) parsed = v6.toIPv4Address();
    }
    return parsed.range() === "unicast";
  } catch {
    return false;
  }
}
