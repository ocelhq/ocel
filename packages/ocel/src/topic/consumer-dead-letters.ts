import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Client } from "@connectrpc/connect";
import { JsonText } from "../delivery/json-text.js";
import { decodeJson, decodeJsonText } from "../delivery/payload.js";
import type {
  DeadLetter as ProtoDeadLetter,
  TopicService,
} from "../gen/proto/app/topic/v1/topic_pb.js";
import type {
  DeadLetter,
  DeadLetterListOptions,
  DeadLetterPage,
  DeadLetters,
} from "./dead-letters.js";

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

export class ConsumerDeadLetters implements DeadLetters {
  readonly consumer: string;
  private readonly connect: (operation: string) => TopicConnection;

  constructor(consumer: string, connect: (operation: string) => TopicConnection) {
    this.consumer = consumer;
    this.connect = connect;
  }

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

  async redrive(executions: string[] = []): Promise<number> {
    const { client, topic } = this.connect("deadLetter.redrive");
    const { redriven } = await client.redriveDeadLetters({
      topic,
      consumer: this.consumer,
      executions,
    });
    return Number(redriven);
  }

  async purge(executions: string[] = []): Promise<number> {
    const { client, topic } = this.connect("deadLetter.purge");
    const { purged } = await client.purgeDeadLetters({
      topic,
      consumer: this.consumer,
      executions,
    });
    return Number(purged);
  }

  async count(): Promise<number> {
    const { client, topic } = this.connect("deadLetter.count");
    const { count } = await client.countDeadLetters({ topic, consumer: this.consumer });
    return Number(count);
  }
}
