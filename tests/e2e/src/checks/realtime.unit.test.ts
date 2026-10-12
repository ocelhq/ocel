import { describe, expect, it } from "bun:test";
import { socketRecorder } from "./realtime";

class Opened {
  constructor(
    readonly url: string | URL,
    readonly protocols?: string | string[],
  ) {}

  close(): void {}
}

describe("socketRecorder", () => {
  it("opens each socket at the address the run reached, and records it", () => {
    const recorder = socketRecorder(
      new Map([["wss://edge/socket", "ws://127.0.0.1:9/socket"]]),
      Opened,
    );
    const socket = new recorder.Recorded("wss://edge/socket") as unknown as Opened;
    expect(socket.url).toBe("ws://127.0.0.1:9/socket");
    expect(recorder.opened).toEqual([socket]);
  });

  it("counts only the sockets of its own run, whatever an earlier run left open", () => {
    const earlier = socketRecorder(new Map(), Opened);
    new earlier.Recorded("wss://edge/socket");
    const later = socketRecorder(new Map(), Opened);
    new later.Recorded("wss://edge/socket");
    new later.Recorded("wss://edge/socket");
    expect(later.opened).toHaveLength(2);
  });
});
