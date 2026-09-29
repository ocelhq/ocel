import { XIcon } from "@phosphor-icons/react";
import { role } from "../lib/type";
import { cn } from "../lib/utils";
import { folderName, names, plural } from "../model";
import { useValue } from "../signals";
import { dismissDrop, dropped } from "../store";
import { Note } from "./Chip";
import { Button } from "./ui/button";

function Code({ children }: { children: React.ReactNode }) {
  return <code className={cn(role.mono, "bg-chip px-1 text-foreground")}>{children}</code>;
}

export function DropNotice() {
  const drop = useValue(dropped);
  if (!drop) return null;
  const where = folderName(drop.folder);
  return (
    <Note role="status" data-slot="notice" label="Imported" className="relative mb-6 pr-12">
      <p>
        <Code>{drop.name}</Code>:{" "}
        {drop.fills.length === 0
          ? `nothing to fill in ${where}.`
          : `${plural(drop.fills.length, "row")} filled in ${where}, unsaved until you save.`}
      </p>
      {drop.undeclared.length > 0 && (
        <p>
          Ignored {plural(drop.undeclared.length, "key")} this project does not declare:{" "}
          <Code>{names(drop.undeclared)}</Code>. Keys come from <Code>defineEnv</Code> in app code;
          this page cannot create one.
        </p>
      )}
      {drop.skipped.map((skip) => (
        <p key={skip.key}>
          Skipped {skip.key}: {skip.reason}.
        </p>
      ))}
      <Button
        variant="ghost"
        size="icon-xs"
        className="absolute top-3 right-3"
        title="Dismiss"
        aria-label="dismiss this notice"
        onClick={dismissDrop}
      >
        <XIcon />
      </Button>
    </Note>
  );
}
