import type { JSX } from "preact";

import {
  names,
  owedChildren,
  plural,
  readBy,
  sameAddress,
  type KeyRow,
  type Variant,
  type VariantOption,
} from "../model";
import {
  addVariant,
  address,
  erase,
  expanded,
  hoveredApp,
  saving,
  selected,
  toggle,
  tree,
} from "../store";
import { Icon } from "./Icons";

export function Table() {
  return (
    <div class="scroller">
      <table class="vars">
        <thead>
          <tr>
            <th scope="col" class="cell-key">
              variable
            </th>
            <th scope="col" class="cell-class">
              class
            </th>
            <th scope="col" class="cell-value">
              value
            </th>
            <th scope="col" class="cell-tools">
              <span class="visually-hidden">actions</span>
            </th>
          </tr>
        </thead>
        {tree.value.map((row) => (
          <KeyGroup row={row} key={row.key} />
        ))}
      </table>
    </div>
  );
}

function interactive(event: Event): boolean {
  return (
    event.target instanceof Element &&
    event.target.closest("button, select, input, a, label") !== null
  );
}

function pickable(variant: Variant): boolean {
  return variant.state !== "forbidden" || variant.set;
}

type RowProps = JSX.HTMLAttributes<HTMLTableRowElement> & {
  "data-selected"?: boolean;
};

function rowProps(variant: Variant): RowProps {
  if (!pickable(variant)) return {};
  const picked = selected.value;
  return {
    tabIndex: 0,
    "data-selected": picked !== null && sameAddress(picked, variant.at),
    onClick: (event) => {
      if (!interactive(event)) address(variant.at);
    },
    onKeyDown: (event) => {
      if (event.key !== "Enter" || interactive(event)) return;
      event.preventDefault();
      address(variant.at);
    },
  };
}

function KeyGroup({ row }: { row: KeyRow }) {
  const open = expanded.value.has(row.key);
  const owed = owedChildren(row);
  const app = hoveredApp.value;
  return (
    <tbody
      class="group"
      data-expanded={open}
      data-dim={app !== null && !readBy(row, app)}
    >
      <tr
        class="row parent"
        data-owed={row.root.owed}
        aria-expanded={open}
        {...rowProps(row.root)}
      >
        <th scope="row" class="cell-key">
          <button
            type="button"
            class="toggle"
            aria-expanded={open}
            aria-label={`${open ? "collapse" : "expand"} ${row.key}`}
            onClick={() => toggle(row.key)}
          >
            <Icon name="chevron" />
          </button>
          <span class="key">{row.key}</span>
          {row.children.length > 0 && (
            <span class="count" data-owed={owed > 0}>
              {plural(row.children.length, "variant")}
              {owed > 0 && ` · ${owed} owed`}
            </span>
          )}
        </th>
        <td class="cell-class">
          {row.class}
          {row.scope.length > 0 && (
            <span class="scope"> · scoped to {names(row.scope)}</span>
          )}
        </td>
        <td class="cell-value">
          <Value variant={row.root} scope={row.scope} />
        </td>
        <td class="cell-tools" />
      </tr>
      {open &&
        row.children.map((child) => (
          <ChildRow variant={child} key={`${child.at.folder} ${child.at.environment}`} />
        ))}
      {open && <AddRow row={row} />}
    </tbody>
  );
}

function ChildRow({ variant }: { variant: Variant }) {
  const { folder, environment } = variant.at;
  return (
    <tr
      class="row child"
      data-kind={variant.kind}
      data-owed={variant.owed}
      data-orphaned={variant.orphaned}
      {...rowProps(variant)}
    >
      <th scope="row" class="cell-key">
        {folder !== "" && (
          <span class="badge" data-kind="folder">
            <Icon name="folder" />
            {folder}
          </span>
        )}
        {environment !== "" && (
          <span
            class="badge"
            data-kind="environment"
            data-tone={variant.orphaned ? "owed" : undefined}
          >
            <Icon name="environment" />
            {environment}
          </span>
        )}
        {variant.orphaned && (
          <span class="badge" data-tone="owed">
            <Icon name="warning" />
            orphaned
          </span>
        )}
      </th>
      <td class="cell-class" />
      <td class="cell-value">
        <Value variant={variant} scope={[]} />
      </td>
      <td class="cell-tools">
        {variant.orphaned && (
          <button
            type="button"
            class="iconbtn"
            data-tone="owed"
            title={`remove the ${environment} value — ${environment} no longer exists`}
            aria-label={`remove the ${environment} value of ${variant.at.key}`}
            disabled={saving.value}
            onClick={() => void erase(variant.at, variant.version)}
          >
            <Icon name="trash" />
          </button>
        )}
      </td>
    </tr>
  );
}

function Value({ variant, scope }: { variant: Variant; scope: string[] }) {
  if (variant.state === "forbidden" && !variant.set) {
    return (
      <span class="unset">
        {variant.kind === "root" && scope.length > 0
          ? `scoped to ${names(scope)}`
          : "nothing reads it here"}
      </span>
    );
  }
  return (
    <>
      {variant.set ? (
        <span class="stored">set · v{variant.version}</span>
      ) : variant.owed ? (
        <span class="owed-text">required, not set</span>
      ) : (
        <span class="unset">not set</span>
      )}
      {variant.orphaned && (
        <span class="fault">
          nothing reads it — {variant.at.environment} no longer exists
        </span>
      )}
      {variant.problem && (
        <span class="fault">
          <Icon name="warning" />
          fails its schema: {variant.problem}
        </span>
      )}
    </>
  );
}

function AddRow({ row }: { row: KeyRow }) {
  const folders = row.options.filter((option) => option.at.environment === "");
  const environments = row.options.filter(
    (option) => option.at.environment !== "",
  );
  const choose = (event: JSX.TargetedEvent<HTMLSelectElement>) => {
    const option = row.options[Number(event.currentTarget.value)];
    event.currentTarget.value = "";
    if (option) addVariant(option.at);
  };
  const group = (label: string, items: VariantOption[]) =>
    items.length > 0 && (
      <optgroup label={label}>
        {items.map((option) => (
          <option value={row.options.indexOf(option)} key={option.label}>
            {option.label}
          </option>
        ))}
      </optgroup>
    );
  return (
    <tr class="row add">
      <td colspan={4}>
        {row.options.length === 0 ? (
          <span class="unset">every folder and environment already has a row</span>
        ) : (
          <label class="add-variant">
            <Icon name="plus" />
            <span>add a variant</span>
            <select value="" onChange={choose}>
              <option value="" disabled>
                choose a folder or environment
              </option>
              {group("folder", folders)}
              {group("environment", environments)}
            </select>
          </label>
        )}
      </td>
    </tr>
  );
}
