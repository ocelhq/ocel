import { describe, expect, it } from "bun:test";
import { NonzeroExitError, type Ran, recordOutput } from "./ocel";

const PRINTED = "connecting to redis://default:hunter2-in-clear@127.0.0.1:6379";

describe("recordOutput", () => {
  it("keeps what a command that succeeded printed", async () => {
    const said: string[] = [];
    await recordOutput(said, Promise.resolve({ code: 0, stdout: PRINTED, stderr: "warned" }));
    expect(said).toEqual([PRINTED, "warned"]);
  });

  it("keeps what a command that failed printed, unredacted, and still fails", async () => {
    const said: string[] = [];
    const ran: Ran = { code: 1, stdout: PRINTED, stderr: "refused" };
    const failed = recordOutput(said, Promise.reject(new NonzeroExitError(["deploy"], ran)));
    await expect(failed).rejects.toThrow("ocel deploy exited 1");
    expect(said).toEqual([PRINTED, "refused"]);
  });
});
