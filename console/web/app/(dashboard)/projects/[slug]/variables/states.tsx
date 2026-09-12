import type { RefusalReason } from "@console/connectors";
import type { ReactNode } from "react";
import { CommandPane } from "../../../command-pane";
import { PageNotice } from "../../../page-shell";

function Notice({
  heading,
  children,
  role,
}: {
  heading: string;
  children: ReactNode;
  role?: "alert";
}) {
  return (
    <PageNotice>
      <div className="flex flex-col items-start gap-4" role={role}>
        <h2 className="text-base font-semibold">{heading}</h2>
        {children}
      </div>
    </PageNotice>
  );
}

export function NeverDeployed() {
  return (
    <Notice heading="No variables declared yet">
      <p className="max-w-prose text-sm text-muted-foreground">
        Keys come from <code className="font-mono text-foreground">defineEnv</code> in your app
        code, and the console learns them when a deploy reports what it landed. Nothing has been
        reported for this project.
      </p>
      <CommandPane command="ocel deploy" />
    </Notice>
  );
}

export function NoConnector({ vendor }: { vendor: string | null }) {
  return (
    <Notice heading="Values live in your own cloud">
      <p className="max-w-prose text-sm text-muted-foreground">
        The keys below are what your last deploy reported. The values themselves are stored in
        {vendor ? ` your ${vendor} account` : " your own cloud"} and the console never holds them,
        so reading one needs a connector running there.
      </p>
      <CommandPane command="ocel console connector add" />
    </Notice>
  );
}

const refusals: Record<RefusalReason, { heading: string; body: string; command?: string }> = {
  offline: {
    heading: "The connector isn’t answering",
    body: "Nothing in your cloud is affected, and your values are untouched. The keys below are what the last deploy reported.",
    command: "ocel console connector status",
  },
  incompatible: {
    heading: "The connector is a different version",
    body: "This console and the connector in your account no longer agree on the contract, so it refused rather than guess.",
    command: "ocel console connector add",
  },
  denied: {
    heading: "The connector refused",
    body: "Its credentials in your account do not permit reading variables. Nothing was changed.",
    command: "ocel console connector status",
  },
  "lost-lease": {
    heading: "The request timed out",
    body: "The connector took the work and never reported back. Nothing was retried, so nothing ran twice.",
  },
  failed: {
    heading: "The connector failed",
    body: "It answered, but could not complete the request. Nothing in your cloud was changed.",
  },
};

export function Refused({ reason, message }: { reason: RefusalReason; message: string }) {
  const held = refusals[reason];
  return (
    <Notice heading={held.heading} role="alert">
      <p className="max-w-prose text-sm text-muted-foreground">{held.body}</p>
      <p className="max-w-prose font-mono text-xs break-words text-muted-foreground">{message}</p>
      {held.command && <CommandPane command={held.command} />}
    </Notice>
  );
}

export function LoadError() {
  return (
    <Notice heading="Variables didn’t load" role="alert">
      <p className="max-w-prose text-sm text-muted-foreground">
        Nothing in your cloud is affected. Try again in a moment.
      </p>
    </Notice>
  );
}
