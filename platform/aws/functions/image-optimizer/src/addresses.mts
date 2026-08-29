import ipaddr from "ipaddr.js";

const IPV4_COMPATIBLE = ipaddr.parse("::") as ipaddr.IPv6;

export function isReachableAddress(address: string): boolean {
  const bare =
    address.startsWith("[") && address.endsWith("]")
      ? address.slice(1, -1)
      : address;

  if (!ipaddr.isValid(bare)) return false;

  try {
    let parsed = ipaddr.parse(bare);
    if (parsed.kind() === "ipv6") {
      const v6 = parsed as ipaddr.IPv6;
      if (v6.isIPv4MappedAddress()) parsed = v6.toIPv4Address();
      else if (v6.match(IPV4_COMPATIBLE, 96)) {
        parsed = ipaddr.fromByteArray(v6.toByteArray().slice(12));
      }
    }
    return parsed.range() === "unicast";
  } catch {
    return false;
  }
}
