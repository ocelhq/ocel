import { describe, expect, test } from "vitest";
import { isReachableAddress } from "../src/addresses.mjs";

// The corpus is Next's own packages/next/src/server/is-private-ip.test.ts,
// lifted verbatim from v16.2.10 and inverted: this function answers "may I
// connect to this?" where Next's answers "is this private?". Keeping their
// inputs means the ranges someone already thought to attack are the ranges
// asserted here, rather than the ones we happened to think of.
describe("the addresses Next's corpus calls private", () => {
  const PRIVATE = [
    "127.0.0.0",
    "127.0.0.1",
    // Zero-padded octets. The URL parser normalizes these; ipaddr.js also
    // parses them, so neither layer is the only thing catching them.
    "127.0.0.01",
    "127.0.0.001",
    "0.0.0.0",
    "10.0.0.0",
    "10.244.0.0",
    "192.168.0.0",
    "192.168.0.1",
    "192.168.0.01",
    "172.16.0.0",
    "172.16.0.1",
    "172.16.0.01",
    // The instance metadata service. An SSRF that reaches this address gets
    // this function's IAM role, and with it every tenant's assets.
    "169.254.169.254",
    "::",
    "::1",
    "::ffff:0.0.0.0",
    // Loopback wearing an IPv6 costume, in both notations.
    "::ffff:127.0.0.1",
    "::ffff:7f00:1",
    "2001:2f:ffff:ffff:ffff:ffff:ffff:ffff",
    "[2001:2f:ffff:ffff:ffff:ffff:ffff:ffff]",
    // 6to4 and multicast.
    "2002::",
    "ff00::",
  ];

  for (const address of PRIVATE) {
    test(`${address} is unreachable`, () => {
      expect(isReachableAddress(address)).toBe(false);
    });
  }

  // Next reaches this one through the URL parser rather than as a literal,
  // because 0x7f000001 is not an address until something normalizes it.
  test("0x7f000001 normalized through new URL is unreachable", () => {
    expect(isReachableAddress(new URL("http://0x7f000001").hostname)).toBe(false);
  });
});

describe("the addresses Next's corpus calls public", () => {
  for (const address of [
    "76.76.21.21",
    "157.240.14.35",
    "8.8.8.8",
    "1.1.1.1",
    "::ffff:8.8.8.8",
    "::ffff:1.1.1.1",
    "2001:4860:4860::8888",
    "2606:4700:4700::1111",
  ]) {
    test(`${address} is reachable`, () => {
      expect(isReachableAddress(address)).toBe(true);
    });
  }
});

// Next's third group asserts that hostnames are "not private", because its
// helper is asked about names. This one is only ever asked about resolved
// addresses, so a name is not a thing it can approve — and the default-deny
// posture is what makes that safe rather than surprising.
describe("anything that is not an address", () => {
  for (const value of ["vercel.com", "www.vercel.com", "nextjs.org", "", "not-an-ip"]) {
    test(`${JSON.stringify(value)} is unreachable`, () => {
      expect(isReachableAddress(value)).toBe(false);
    });
  }
});

// The blocks a hand-rolled CIDR list forgets, which is the argument for asking
// ipaddr.js for "unicast" instead of enumerating anything.
describe("blocks a hand-rolled list would miss", () => {
  const cases: Array<[string, string]> = [
    ["100.64.0.1", "CGNAT 100.64/10"],
    ["192.0.2.1", "TEST-NET-1"],
    ["198.51.100.1", "TEST-NET-2"],
    ["203.0.113.1", "TEST-NET-3"],
    ["198.18.0.1", "benchmarking"],
    ["192.88.99.1", "6to4 relay anycast"],
    ["224.0.0.1", "multicast"],
    ["255.255.255.255", "broadcast"],
    ["240.0.0.1", "reserved"],
    ["169.254.1.1", "link-local"],
    ["fc00::1", "ULA fc00::/7"],
    ["fd00::1", "ULA fd00::/8"],
    ["fe80::1", "link-local"],
    ["64:ff9b::1.2.3.4", "NAT64"],
    ["2001::1", "Teredo"],
    ["100::1", "discard"],
  ];
  for (const [address, why] of cases) {
    test(`${address} (${why})`, () => {
      expect(isReachableAddress(address)).toBe(false);
    });
  }
});

// The three notations the design names, each normalized by new URL() before any
// check runs. Without that normalization every one of them is merely
// "unparseable" rather than "loopback" — the same answer here, but for the wrong
// reason, and a reason that stops holding the moment a caller passes a hostname
// through some other parser.
describe("obfuscated loopback, normalized first", () => {
  for (const host of ["0x7f000001", "2130706433", "0177.0.0.1"]) {
    test(host, () => {
      const hostname = new URL(`http://${host}/x.png`).hostname;
      expect(hostname).toBe("127.0.0.1");
      expect(isReachableAddress(hostname)).toBe(false);
    });
  }
});
