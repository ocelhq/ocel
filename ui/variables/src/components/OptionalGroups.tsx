import { Menu as MenuPrimitive } from "@base-ui/react/menu";
import { CaretDownIcon, CheckIcon, MinusIcon } from "@phosphor-icons/react";
import { role } from "../lib/type";
import { cn } from "../lib/utils";
import { folderName, names, type OptionalGroup, plural } from "../model";
import { useValue } from "../signals";
import { ability, optionalGroups, state, switchVariableGroup } from "../store";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "./ui/dropdown-menu";

const label = cn(role.label, "text-muted-foreground");
const note = cn(role.meta, "text-muted-foreground");

function status(group: OptionalGroup): { text: string; warn: boolean } {
  if (group.on === "off") {
    return group.removing > 0
      ? { text: `removes ${plural(group.removing, "value")} on save`, warn: false }
      : { text: plural(group.keys, "key"), warn: false };
  }
  if (group.missing > 0) return { text: `${group.missing} to fill`, warn: true };
  if (group.on === "mixed") {
    return { text: `on in ${names(group.onIn.map(folderName))}`, warn: false };
  }
  return { text: "complete", warn: false };
}

function Box({ on }: { on: OptionalGroup["on"] }) {
  return (
    <span
      aria-hidden
      className={cn(
        "mt-0.5 flex size-4 shrink-0 items-center justify-center border transition-colors",
        on === "off"
          ? "border-input bg-background group-hover/group-item:border-foreground"
          : "border-primary bg-primary text-primary-foreground",
      )}
    >
      {on === "on" && <CheckIcon weight="bold" className="size-3" />}
      {on === "mixed" && <MinusIcon weight="bold" className="size-3" />}
    </span>
  );
}

function Item({ group, shut }: { group: OptionalGroup; shut: boolean }) {
  const said = status(group);
  return (
    <MenuPrimitive.CheckboxItem
      data-slot="optional-group"
      data-group={group.group.key}
      data-on={group.on}
      checked={group.on === "on"}
      aria-checked={group.on === "mixed" ? "mixed" : group.on === "on"}
      disabled={shut}
      closeOnClick={false}
      onCheckedChange={() => switchVariableGroup(group.group.key, group.on !== "on")}
      className="group/group-item flex cursor-default items-start gap-3 px-3 py-2.5 outline-hidden select-none focus:bg-accent data-disabled:pointer-events-none data-disabled:opacity-50"
    >
      <Box on={group.on} />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-baseline gap-3">
          <span className={cn(role.path, "min-w-0 flex-1 truncate text-foreground")}>
            {group.group.key}
          </span>
          <span
            data-slot="optional-group-status"
            className={cn(
              role.meta,
              "shrink-0 tabular-nums",
              said.warn
                ? "text-warn"
                : group.removing > 0
                  ? "text-foreground"
                  : "text-muted-foreground",
            )}
          >
            {said.text}
          </span>
        </span>
        {group.group.description && (
          <span title={group.group.description} className={cn(note, "line-clamp-2 text-body")}>
            {group.group.description}
          </span>
        )}
        {group.on !== "off" && group.stored > 0 && (
          <span className={note}>switching off removes {plural(group.stored, "saved value")}</span>
        )}
      </span>
    </MenuPrimitive.CheckboxItem>
  );
}

export function OptionalGroups() {
  const groups = useValue(optionalGroups);
  const current = useValue(state);
  const can = useValue(ability);
  if (groups.length === 0) return null;
  const unknown = current?.values === "unknown";
  const shut = unknown || !can.write;
  const on = groups.filter((group) => group.on !== "off").length;
  const toFill = groups.reduce((sum, group) => sum + (group.on === "off" ? 0 : group.missing), 0);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        data-action="optional-groups"
        aria-label={`optional groups, ${on} of ${groups.length} on`}
        className={cn(
          role.path,
          "flex h-8 w-fit items-center gap-2 rounded-none border border-input bg-transparent px-2.5 whitespace-nowrap transition-colors outline-none hover:bg-muted focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50 data-popup-open:bg-muted",
        )}
      >
        <span className={cn(label, "shrink-0")}>optional groups</span>
        <span className="tabular-nums">
          {on === 0 ? "none on" : `${on} of ${groups.length} on`}
        </span>
        {toFill > 0 && (
          <span
            data-slot="optional-groups-to-fill"
            className={cn(role.meta, "text-warn tabular-nums")}
          >
            {toFill} to fill
          </span>
        )}
        <CaretDownIcon aria-hidden className="size-3.5 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-96 py-1">
        <MenuPrimitive.Group>
          <MenuPrimitive.GroupLabel className="flex flex-col gap-1 px-3 pt-2 pb-1.5">
            <span className={label}>Optional groups</span>
            <span className={cn(note, "normal-case")}>
              {unknown
                ? "the console cannot read these values"
                : !can.write
                  ? "your role cannot change these values"
                  : "Switch a group on to fill its keys; switching one off removes its values when you save."}
            </span>
          </MenuPrimitive.GroupLabel>
          {groups.map((group) => (
            <Item group={group} shut={shut} key={group.group.key} />
          ))}
        </MenuPrimitive.Group>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
