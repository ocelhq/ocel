import { describe, expect, it } from "bun:test";
import { dispatchArgs, tasksListenAddresses } from "./dispatch";

describe("dispatchArgs", () => {
  it("listens for tasks on every address it is handed", () => {
    const args = dispatchArgs("http://127.0.0.1:4588", "floci-local", "europe-west1", [
      "127.0.0.1:7001",
      "172.17.0.1:7001",
    ]);

    expect(args.slice(-2)).toEqual(["-tasks-listen", "127.0.0.1:7001,172.17.0.1:7001"]);
  });

  it("passes the dispatcher only its emulator, project and region when it listens for no tasks", () => {
    const args = dispatchArgs("http://127.0.0.1:4588", "floci-local", "europe-west1");

    expect(args).toEqual([
      "-endpoint",
      "http://127.0.0.1:4588",
      "-project",
      "floci-local",
      "-region",
      "europe-west1",
    ]);
  });
});

describe("tasksListenAddresses", () => {
  it("listens for tasks on loopback, and on the docker gateway when there is one", () => {
    expect(tasksListenAddresses(7001, undefined)).toEqual(["127.0.0.1:7001"]);
    expect(tasksListenAddresses(7001, "172.17.0.1")).toEqual(["127.0.0.1:7001", "172.17.0.1:7001"]);
  });
});
