import { TrashIcon } from "@phosphor-icons/react";
import { role } from "../lib/type";
import { cn } from "../lib/utils";
import { addressKey, plural } from "../model";
import { useValue } from "../signals";
import {
  askRemoval,
  clearSelection,
  hideSelected,
  revealSelected,
  saving,
  selected,
  variants,
} from "../store";
import { Button } from "./ui/button";

export function BulkBar() {
  const picked = useValue(selected);
  const known = useValue(variants);
  const busy = useValue(saving);
  if (picked.size === 0) return null;
  const cells = [...picked]
    .map((key) => known.get(key))
    .filter((v) => v !== undefined)
    .map((v) => v!.at);
  const removable = cells.filter((at) => {
    const v = known.get(addressKey(at));
    return v?.set && !v.reference;
  });
  return (
    <div
      role="toolbar"
      aria-label="selected rows"
      data-slot="bulk"
      className="sticky bottom-0 z-20 flex flex-wrap items-center gap-2 border-t border-border bg-background px-6 py-2.5"
    >
      <span className={cn(role.body, "tabular-nums")}>{picked.size} selected</span>
      <Button variant="ghost" size="xs" data-action="unselect" onClick={clearSelection}>
        Unselect all
      </Button>
      <span className="flex-1" />
      <Button variant="outline" size="xs" onClick={() => void revealSelected()}>
        Reveal
      </Button>
      <Button variant="outline" size="xs" onClick={hideSelected}>
        Hide
      </Button>
      <Button
        variant="destructive"
        size="xs"
        data-action="remove"
        disabled={removable.length === 0 || busy}
        onClick={() => askRemoval(removable)}
      >
        <TrashIcon />
        Remove {removable.length > 0 ? plural(removable.length, "value") : "values"}
      </Button>
    </div>
  );
}
