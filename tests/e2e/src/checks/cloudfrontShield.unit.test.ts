import { describe, expect, it } from "bun:test";
import https from "node:https";
import type { AddressInfo, Socket } from "node:net";
import {
  addressesOf,
  airportOf,
  lookupAt,
  rendersOf,
  settledInOneRender,
  sharedAirports,
} from "./cloudfrontShield";

const page = (render: string) =>
  `<main><div><span data-ocel="shield:cached">${render}</span></div></main>`;

describe("addressesOf", () => {
  it("keeps the IPv4 addresses of a DNS answer and nothing else", () => {
    const answer = {
      Answer: [
        { type: 5, data: "d111111abcdef8.cloudfront.net." },
        { type: 1, data: "18.66.1.2" },
        { type: 1, data: "18.66.1.3" },
      ],
    };

    expect(addressesOf(answer)).toEqual(["18.66.1.2", "18.66.1.3"]);
  });

  it("is empty for an answer that names no address", () => {
    expect(addressesOf({})).toEqual([]);
  });
});

describe("rendersOf", () => {
  it("reads the render each page body was stamped with", () => {
    expect(rendersOf([page("a"), page("b"), page("a")])).toEqual(["a", "b", "a"]);
  });
});

describe("settledInOneRender", () => {
  it("accepts bodies that all carry one render", () => {
    expect(settledInOneRender([page("a"), page("a")])).toBe(true);
  });

  it("refuses bodies that carry two renders: the function ran twice", () => {
    expect(settledInOneRender([page("a"), page("b")])).toBe(false);
  });
});

describe("airportOf", () => {
  it("reads the airport code that names the edge location of a pop", () => {
    expect(airportOf("DUB56-P1")).toBe("DUB");
    expect(airportOf("nrt57-p2")).toBe("NRT");
  });
});

describe("sharedAirports", () => {
  it("is empty when each vantage was served from edge locations of its own", () => {
    expect(sharedAirports([["DUB56-P1", "DUB2-C1"], ["NRT57-P2"]])).toEqual([]);
  });

  it("names the airport two vantages were both served from, whatever the pop's number", () => {
    expect(sharedAirports([["IAD89-P1"], ["IAD12-C2", "NRT57-P2"]])).toEqual(["IAD"]);
  });
});

describe("lookupAt", () => {
  it("answers a lookup that asks for every address with a list", () => {
    let got: unknown;
    lookupAt("18.66.1.2")("shop.example.com", { all: true }, (_error, address) => {
      got = address;
    });

    expect(got).toEqual([{ address: "18.66.1.2", family: 4 }]);
  });

  it("answers a lookup that asks for one address with that address", () => {
    let got: unknown[] = [];
    lookupAt("18.66.1.2")("shop.example.com", {}, (_error, address, family) => {
      got = [address, family];
    });

    expect(got).toEqual(["18.66.1.2", 4]);
  });

  it("connects an https request to the address it pins", async () => {
    const server = https.createServer();
    const connected = new Promise<string>((resolve) =>
      server.on("connection", (socket: Socket) => resolve(socket.localAddress ?? "")),
    );
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address() as AddressInfo;
    const req = https.request({
      host: "shop.example.com",
      port,
      lookup: lookupAt("127.0.0.1") as never,
    });
    req.on("error", () => {});
    req.end();

    try {
      expect(await connected).toBe("127.0.0.1");
    } finally {
      req.destroy();
      server.close();
    }
  });
});
