"use client";

import {
  BulkBar,
  Confirm,
  CopyPanel,
  Drawer,
  DropNotice,
  install,
  SaveBar,
  type State,
  store,
  Table,
  useValue,
} from "@ui/vars";
import { useEffect, useState } from "react";

import { consolePort } from "./port";

export function VariablesTable({
  projectId,
  environment,
  initial,
  readOnly,
}: {
  projectId: string;
  environment: string;
  initial: State;
  readOnly: boolean;
}) {
  const [ready, setReady] = useState(false);

  useEffect(() => {
    install(consolePort(projectId, environment));
    store.state.value = initial;
    setReady(true);
  }, [projectId, environment, initial]);

  const current = useValue(store.state);
  if (!ready || !current) {
    return null;
  }

  return (
    <div data-vars data-read-only={readOnly || undefined} className="flex flex-1 flex-col gap-4">
      <DropNotice />
      <Table />
      {!readOnly && <BulkBar />}
      {!readOnly && <SaveBar />}
      <Drawer />
      <CopyPanel />
      <Confirm />
    </div>
  );
}
