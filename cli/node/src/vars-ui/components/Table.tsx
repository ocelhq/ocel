import type { JSX } from "preact";

import {
  addressKey,
  baselineOf,
  isDirty,
  names,
  owedChildren,
  plural,
  readBy,
  type KeyRow,
  type Variant,
  type VariantOption,
} from "../model";
import {
  addVariant,
  baselines,
  drafts,
  dropVariant,
  erase,
  expanded,
  hoveredApp,
  problems,
  saving,
  setDraft,
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

function editable(variant: Variant): boolean {
  return variant.state !== "forbidden" || variant.set;
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
        data-dirty={isDirty(row.root.at, drafts.value, baselines.value)}
        aria-expanded={open}
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
        <td class="cell-tools">
          <Tools variant={row.root} />
        </td>
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
      data-dirty={isDirty(variant.at, drafts.value, baselines.value)}
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
        <Tools variant={variant} />
      </td>
    </tr>
  );
}

function describe(variant: Variant): string {
  const { key, folder, environment } = variant.at;
  const where = folder === "" ? "the root" : folder;
  return environment === ""
    ? `${key} in ${where}`
    : `${key} in ${where} for ${environment}`;
}

function Value({ variant, scope }: { variant: Variant; scope: string[] }) {
  if (!editable(variant)) {
    return (
      <span class="unset">
        {variant.kind === "root" && scope.length > 0
          ? `scoped to ${names(scope)}`
          : "nothing reads it here"}
      </span>
    );
  }
  const key = addressKey(variant.at);
  const draft = drafts.value.get(key) ?? baselineOf(variant.at, baselines.value);
  const dirty = isDirty(variant.at, drafts.value, baselines.value);
  const problem = problems.value.get(key);
  return (
    <div class="value" data-dirty={dirty}>
      <div class="value-line">
        <span class="status">
          {variant.set ? (
            <span class="stored">set · v{variant.version}</span>
          ) : variant.owed ? (
            <span class="owed-text">required</span>
          ) : (
            <span class="unset">not set</span>
          )}
        </span>
        <input
          type="text"
          class="value-input"
          value={draft}
          autocomplete="off"
          spellcheck={false}
          disabled={variant.orphaned}
          placeholder={variant.set ? "replace the value that is set" : ""}
          aria-label={`value of ${describe(variant)}`}
          onInput={(event) => setDraft(variant.at, event.currentTarget.value)}
        />
        {dirty && <span class="tag">unsaved</span>}
        {problem && (
          <span class="tag" data-tone="owed">
            {problem.kind === "conflict" ? "conflict" : "failed"}
          </span>
        )}
      </div>
      {problem && (
        <span class="fault">
          <Icon name="warning" />
          {problem.kind === "conflict"
            ? `This value changed since the page read it — it is now v${variant.version}. Save again to replace what is there now.`
            : problem.message}
        </span>
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
    </div>
  );
}

function Tools({ variant }: { variant: Variant }) {
  const busy = saving.value;
  if (variant.extra) {
    return (
      <button
        type="button"
        class="iconbtn"
        title="dismiss this empty row"
        aria-label={`dismiss the empty row for ${describe(variant)}`}
        onClick={() => dropVariant(variant.at)}
      >
        <Icon name="x" />
      </button>
    );
  }
  if (!variant.set) return null;
  return (
    <button
      type="button"
      class="iconbtn"
      data-tone={variant.orphaned ? "owed" : undefined}
      title={
        variant.orphaned
          ? `remove the ${variant.at.environment} value — ${variant.at.environment} no longer exists`
          : "remove this value"
      }
      aria-label={`remove the value of ${describe(variant)}`}
      disabled={busy}
      onClick={() => void erase(variant.at, variant.version)}
    >
      <Icon name="trash" />
    </button>
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
