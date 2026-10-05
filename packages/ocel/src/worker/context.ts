import type { JsonText } from "../delivery/json-text.js";

/** What a run, its hooks and every middleware know about the attempt in progress. */
export interface RunContext {
  /** Whether a task's run or a topic consumer is being served. */
  kind: "task" | "consumer";
  /** The task's name, or the consumer's. */
  name: string;
  /** The topic the message was sent to; a task's topic is the task's own name. */
  topic: string;
  /** The run's id for a task, the execution's for a consumer; a batch's first message's. */
  id: string;
  /** Which attempt this is. */
  attempt: {
    /** This attempt's number, the first being 1. */
    number: number;
    /** How many attempts are made at most. */
    of: number;
    /** When the first attempt started. */
    firstAttemptedAt: Date | undefined;
  };
  /** The message being served; a batch's first message. */
  message: {
    /** The message's id. */
    id: string;
    /** When the message was sent. */
    publishedAt: Date | undefined;
  };
  /** Aborted when the run is canceled while this attempt is in progress. */
  signal: AbortSignal;
}

/** The second argument a run or a consumer receives. */
export interface RunOptions {
  /** The attempt in progress. */
  ctx: RunContext;
  /** Aborted when the run is canceled while this attempt is in progress. */
  signal: AbortSignal;
  /** The JSON text the payload was sent as, byte for byte; a batch's is the list of its payloads' texts. */
  payloadJson: JsonText;
}
