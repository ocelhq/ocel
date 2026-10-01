import { Lane as ProtoLane } from "../gen/proto/app/topic/v1/topic_pb.js";

/** The lane a message waits in: `high`, `default` and `low` are read in a 6:3:1 ratio. */
export type Lane = "high" | "default" | "low";

const protoLanes: Record<Lane, ProtoLane> = {
  high: ProtoLane.HIGH,
  default: ProtoLane.DEFAULT,
  low: ProtoLane.LOW,
};

export function encodeLane(lane: Lane | undefined): ProtoLane {
  if (lane === undefined) return ProtoLane.UNSPECIFIED;
  const value = protoLanes[lane];
  if (value === undefined) {
    throw new Error(`"${lane}" is not a lane: use "high", "default" or "low"`);
  }
  return value;
}
