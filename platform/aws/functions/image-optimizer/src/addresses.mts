import ipaddr from "ipaddr.js";

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
    }
    return parsed.range() === "unicast";
  } catch {
    return false;
  }
}
