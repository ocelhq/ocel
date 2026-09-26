import { ArrowUUpLeftIcon } from "@phosphor-icons/react";
import {
  BulkBar,
  Button,
  Confirm,
  CopyPanel,
  Drawer,
  DropNotice,
  Note,
  plural,
  role,
  SaveBar,
  store,
  Table,
  useValue,
} from "@ui/vars";

import { cn } from "../lib/utils";
import { Masthead } from "./Masthead";

function Code({ children }: { children: React.ReactNode }) {
  return <code className={cn(role.mono, "bg-chip px-1 text-foreground")}>{children}</code>;
}

function Banner() {
  const current = useValue(store.state);
  const left = useValue(store.unfilled).length;
  const _only = useValue(store.unfilledOnly);
  const recovery = current?.recovery;
  if (!current || !recovery) return null;
  return (
    <Note role="status" data-slot="banner" label="Deploy waiting" className="mb-6">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
        <p className="flex-1 basis-96">
          Deploy <Code>{recovery.deploy}</Code> was refused{" "}
          {plural(recovery.missing.length, "cell")}.{" "}
          {left === 0
            ? "All filled — save and resume below."
            : `${plural(left, "cell")} still empty.`}
        </p>
      </div>
    </Note>
  );
}

function Resume() {
  const busySaving = useValue(store.saving);
  const busyFinishing = useValue(store.finishing);
  const left = useValue(store.unfilled).length;
  const busy = busySaving || busyFinishing;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        size="sm"
        data-action="resume"
        disabled={busy || left > 0}
        title={left > 0 ? `${plural(left, "cell")} still empty or invalid` : undefined}
        onClick={() => void store.resume()}
      >
        <ArrowUUpLeftIcon />
        {busyFinishing ? "Resuming…" : "Save and resume the deploy"}
      </Button>
      <Button variant="destructive" size="sm" disabled={busy} onClick={() => void store.abandon()}>
        Abandon the deploy
      </Button>
    </div>
  );
}

export function App() {
  const goodbye = useValue(store.farewell);
  const current = useValue(store.state);
  const failed = useValue(store.finishError);
  if (goodbye !== null) {
    return <p className={cn(role.body, "p-12 text-body")}>{goodbye}</p>;
  }
  if (!current) {
    return <p className={cn(role.body, "p-12 text-body")}>Reading variables…</p>;
  }
  const recovery = current.recovery !== undefined;
  return (
    <div className="flex min-h-screen flex-col">
      <div className="mx-auto w-full max-w-[92rem] flex-1 px-10 pt-8 pb-24">
        <Masthead current={current} />
        <Banner />
        <DropNotice />
        <Table />
      </div>
      <BulkBar />
      <SaveBar actions={recovery ? <Resume /> : undefined} note={failed} />
      <Drawer />
      <CopyPanel />
      <Confirm />
    </div>
  );
}
