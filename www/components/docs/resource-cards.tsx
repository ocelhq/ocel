import {
  ArchiveBoxIcon,
  CircleStackIcon,
  KeyIcon,
  MegaphoneIcon,
  QueueListIcon,
  SignalIcon,
} from "@heroicons/react/24/outline";
import { Card, Cards } from "fumadocs-ui/components/card";

export function ResourceCards() {
  return (
    <Cards className="gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <Card
        icon={<CircleStackIcon />}
        title="Postgres"
        href="/docs/sdk/postgres"
        description="A database, handed to your app as a connected pool."
      />
      <Card
        icon={<ArchiveBoxIcon />}
        title="Bucket"
        href="/docs/sdk/bucket"
        description="Object storage your app reads and writes."
      />
      <Card
        icon={<KeyIcon />}
        title="KV"
        href="/docs/sdk/kv"
        description="A key-value store, typed by key pattern."
      />
      <Card
        icon={<SignalIcon />}
        title="Realtime"
        href="/docs/sdk/realtime"
        description="Typed channels browsers subscribe to."
      />
      <Card
        icon={<MegaphoneIcon />}
        title="Topic"
        href="/docs/sdk/topic"
        description="Messages every consumer receives."
      />
      <Card
        icon={<QueueListIcon />}
        title="Task"
        href="/docs/sdk/task"
        description="Background work, run once per trigger."
      />
    </Cards>
  );
}
