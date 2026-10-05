import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Client } from "@connectrpc/connect";
import { JsonText } from "../delivery/json-text.js";
import { decodeJson, decodeJsonText } from "../delivery/payload.js";
import type {
  DeadLetter as ProtoDeadLetter,
  TopicService,
} from "../gen/proto/app/topic/v1/topic_pb.js";

/** A message a consumer gave up on after its last attempt failed. */
export interface DeadLetter {
  /** The execution that failed: the message as this consumer received it. */
  execution: string;
  /** The message's id. */
  messageId: string;
  /** When the message was sent. */
  publishedAt: Date | undefined;
  /** The message's payload. */
  payload: unknown;
  /** The JSON text the message's payload was sent as, byte for byte. */
  payloadJson: JsonText;
  /** How many attempts were made. */
  attempts: number;
  /** The last attempt's error. */
  error: string;
  /** When the last attempt failed. */
  failedAt: Date | undefined;
}

/** One page of dead letters. */
export interface DeadLetterPage {
  /** The dead letters on this page. */
  deadLetters: DeadLetter[];
  /** Passed as `cursor` to read the next page; empty on the last one. */
  nextCursor: string;
}

/** Which page of dead letters to read. */
export interface DeadLetterListOptions {
  /** The `nextCursor` of the previous page. */
  cursor?: string;
  /** The most dead letters on the page. */
  limit?: number;
}

interface TopicConnection {
  client: Client<typeof TopicService>;
  topic: string;
}

function decodeDeadLetter(letter: ProtoDeadLetter): DeadLetter {
  return {
    execution: letter.execution,
    messageId: letter.message?.id ?? "",
    publishedAt: letter.message?.publishedAt
      ? timestampDate(letter.message.publishedAt)
      : undefined,
    payload: decodeJson(letter.payload),
    payloadJson: decodeJsonText(letter.payload) ?? new JsonText("null"),
    attempts: letter.attempts,
    error: letter.error,
    failedAt: letter.failedAt ? timestampDate(letter.failedAt) : undefined,
  };
}

/** The dead letters of one consumer of a topic. */
export class DeadLetters {
  /** The consumer these dead letters belong to. */
  readonly consumer: string;
  private readonly connect: (operation: string) => TopicConnection;

  constructor(consumer: string, connect: (operation: string) => TopicConnection) {
    this.consumer = consumer;
    this.connect = connect;
  }

  /** One page of this consumer's dead letters. */
  async list(options: DeadLetterListOptions = {}): Promise<DeadLetterPage> {
    const { client, topic } = this.connect("deadLetter.list");
    const page = await client.listDeadLetters({
      topic,
      consumer: this.consumer,
      cursor: options.cursor ?? "",
      limit: options.limit ?? 0,
    });
    return { deadLetters: page.deadLetters.map(decodeDeadLetter), nextCursor: page.nextCursor };
  }

  /** Delivers the given executions again, or every dead letter; answers how many. */
  async redrive(executions: string[] = []): Promise<number> {
    const { client, topic } = this.connect("deadLetter.redrive");
    const { redriven } = await client.redriveDeadLetters({
      topic,
      consumer: this.consumer,
      executions,
    });
    return Number(redriven);
  }

  /** Deletes the given executions, or every dead letter; answers how many. */
  async purge(executions: string[] = []): Promise<number> {
    const { client, topic } = this.connect("deadLetter.purge");
    const { purged } = await client.purgeDeadLetters({
      topic,
      consumer: this.consumer,
      executions,
    });
    return Number(purged);
  }

  /** How many dead letters this consumer has. */
  async count(): Promise<number> {
    const { client, topic } = this.connect("deadLetter.count");
    const { count } = await client.countDeadLetters({ topic, consumer: this.consumer });
    return Number(count);
  }
}
