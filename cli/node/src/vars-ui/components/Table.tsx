import { useRef } from "preact/hooks";

import {
  addressKey,
  baselineOf,
  editable,
  folderName,
  isDirty,
  names,
  overrideOptions,
  plural,
  readBy,
  referenceLine,
  revealable,
  type FolderLine,
  type KeyLine,
  type Reference,
  type Variant,
} from "../model";
import {
  addOverride,
  askRemoval,
  baselines,
  catalogue,
  copyLoading,
  copyValue,
  dismiss,
  drafts,
  dragging,
  environment,
  environments,
  folder,
  go,
  hide,
  hoveredApp,
  importFile,
  listing,
  openCopy,
  openDrawer,
  owedOnly,
  pickEnvironment,
  problems,
  reveal,
  revealErrors,
  saving,
  search,
  selectVisible,
  selected,
  setDraft,
  setSearch,
  shown,
  state,
  toggleRevealVisible,
  toggleSelected,
  visible,
  applyDrop,
} from "../store";
import { Icon } from "./Icons";
import { Menu, type MenuSection } from "./Menu";

function carriesFile(event: DragEvent): boolean {
  const types = event.dataTransfer?.types ?? [];
  return [...types].some((type) => type === "Files" || type === "text/plain");
}

export function Table() {
  const current = state.value!;
  const list = listing.value;
  const into = folder.value;
  return (
    <section
      class="card"
      data-dragging={dragging.value}
      onDragOver={(event) => {
        if (!carriesFile(event)) return;
        event.preventDefault();
        event.dataTransfer!.dropEffect = "copy";
        if (!dragging.value) dragging.value = true;
      }}
      onDragLeave={(event) => {
        if (
          event.currentTarget instanceof Element &&
          event.relatedTarget instanceof Node &&
          event.currentTarget.contains(event.relatedTarget)
        ) {
          return;
        }
        dragging.value = false;
      }}
      onDrop={(event) => {
        if (!carriesFile(event)) return;
        event.preventDefault();
        const file = event.dataTransfer?.files[0];
        if (file) {
          importFile(file);
          return;
        }
        applyDrop("the dropped text", event.dataTransfer?.getData("text/plain") ?? "", into);
      }}
    >
      <Toolbar />
      <table class="vars">
        <thead>
          <tr>
            <th scope="col" class="cell-pick">
              <input
                type="checkbox"
                aria-label="select every row"
                checked={visible.value.length > 0 && selected.value.size === visible.value.length}
                disabled={visible.value.length === 0}
                onChange={(event) => selectVisible(event.currentTarget.checked)}
              />
            </th>
            <th scope="col" class="cell-key">
              Name
            </th>
            <th scope="col" class="cell-value">
              Value
            </th>
            <th scope="col" class="cell-tools">
              <button
                type="button"
                class="btn btn-ghost btn-small"
                aria-pressed={shown.value}
                disabled={visible.value.filter(revealable).length === 0}
                onClick={toggleRevealVisible}
              >
                <Icon name={shown.value ? "eyeOff" : "eye"} />
                {shown.value ? "Hide values" : "Reveal values"}
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          {list.folders.map((line) => (
            <FolderRow line={line} key={line.folder} />
          ))}
          {list.keys.map((line) => (
            <KeyRow line={line} flat={list.flat} key={addressKey(line.variant.at)} />
          ))}
        </tbody>
      </table>
      {list.folders.length === 0 && list.keys.length === 0 && <Empty />}
      <footer class="card-foot">
        <span class="counts">
          {!list.flat && (
            <>
              <Icon name="folder" /> {list.folders.length}
              <span class="counts-gap" />
            </>
          )}
          <Icon name="key" /> {list.keys.length}
        </span>
        <span class="hint">
          <Icon name="file" />
          Drop a .env file on this table to fill {folderName(into)} values
        </span>
      </footer>
      {dragging.value && (
        <div class="dropzone" aria-hidden="true">
          <Icon name="upload" />
          <p>Drop to fill {folderName(into)} values</p>
          <p class="dropzone-sub">
            Keys the project declares fill in as unsaved drafts; nothing is written until you save
          </p>
        </div>
      )}
      {current.recovery && list.keys.length === 0 && owedOnly.value && (
        <p class="empty-note">Every cell the deploy needs has a value. Save and resume below.</p>
      )}
    </section>
  );
}

function Toolbar() {
  const current = state.value!;
  const picker = useRef<HTMLInputElement>(null);
  const list = listing.value;
  return (
    <div class="toolbar">
      <label class="select">
        <Icon name="environment" />
        <select
          value={environment.value}
          aria-label="environment"
          onChange={(event) => pickEnvironment(event.currentTarget.value)}
        >
          <option value="">Base</option>
          {environments.value.map((env) => (
            <option value={env.name} key={env.name}>
              {env.orphaned ? `${env.name} · no longer exists` : env.name}
            </option>
          ))}
        </select>
      </label>
      <nav class="crumbs" aria-label="folder">
        <button
          type="button"
          class="crumb"
          aria-current={!list.flat && folder.value === "" ? "page" : undefined}
          onClick={() => go("")}
        >
          <Icon name="home" />
          <span>/</span>
        </button>
        {!list.flat && folder.value !== "" && (
          <>
            <span class="crumb-sep">›</span>
            <span class="crumb" aria-current="page">
              <Icon name="folder" />
              {folder.value.slice(1)}
            </span>
          </>
        )}
        {owedOnly.value && (
          <>
            <span class="crumb-sep">›</span>
            <span class="crumb" aria-current="page" data-tone="owed">
              <Icon name="warning" />
              {plural(list.keys.length, "cell")} the deploy needs
            </span>
          </>
        )}
        {!owedOnly.value && list.flat && (
          <>
            <span class="crumb-sep">›</span>
            <span class="crumb" aria-current="page">
              <Icon name="search" />
              results across every folder
            </span>
          </>
        )}
      </nav>
      <span class="toolbar-gap" />
      <label class="search">
        <Icon name="search" />
        <input
          type="search"
          placeholder="Search by name"
          value={search.value}
          onInput={(event) => setSearch(event.currentTarget.value)}
        />
      </label>
      <input
        type="file"
        accept=".env,text/plain"
        class="visually-hidden"
        ref={picker}
        tabIndex={-1}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          event.currentTarget.value = "";
          if (file) importFile(file);
        }}
      />
      <button type="button" class="btn" onClick={() => picker.current?.click()}>
        <Icon name="upload" />
        Import .env
      </button>
      <button
        type="button"
        class="btn"
        disabled={copyLoading.value}
        onClick={() => void openCopy()}
      >
        <Icon name="copy" />
        {copyLoading.value ? "Reading…" : `Copy from ${current.other}`}
      </button>
    </div>
  );
}

function Empty() {
  const current = state.value!;
  if (search.value.trim() !== "") {
    return <p class="empty-note">No variable is named like “{search.value.trim()}”.</p>;
  }
  if (current.matrix.rows.length === 0) {
    return (
      <p class="empty-note">
        This project declares no variables yet. Keys come from <code>defineEnv</code> in app
        code; this page cannot create one.
      </p>
    );
  }
  return <p class="empty-note">Nothing reads a variable in {folderName(folder.value)}.</p>;
}

function FolderRow({ line }: { line: FolderLine }) {
  return (
    <tr class="row folder-row" onClick={() => go(line.folder)}>
      <td class="cell-pick" />
      <th scope="row" class="cell-key">
        <button
          type="button"
          class="folder-link"
          onClick={(event) => {
            event.stopPropagation();
            go(line.folder);
          }}
        >
          <Icon name="folder" />
          <span class="key">{line.folder.slice(1)}</span>
        </button>
        {line.owed > 0 && (
          <span class="chip" data-tone="owed">
            {plural(line.owed, "cell")} to fill
          </span>
        )}
      </th>
      <td class="cell-value">
        <span class="muted">{plural(line.keys, "key")}</span>
      </td>
      <td class="cell-tools">
        <Icon name="chevron" />
      </td>
    </tr>
  );
}

function describe(variant: Variant): string {
  const { key, folder: where, environment: env } = variant.at;
  const place = where === "" ? "the root" : where;
  return env === "" ? `${key} in ${place}` : `${key} in ${place} for ${env}`;
}

function KeyRow({ line, flat }: { line: KeyLine; flat: boolean }) {
  const { row, variant } = line;
  const key = addressKey(variant.at);
  const app = hoveredApp.value;
  const open = editable(variant);
  const picked = selected.value.has(key);
  return (
    <tr
      class="row key-row"
      data-owed={variant.owed || line.needed}
      data-dirty={isDirty(variant.at, drafts.value, baselines.value)}
      data-selected={picked}
      data-dim={app !== null && !readBy(row, app)}
    >
      <td class="cell-pick">
        {open && (
          <input
            type="checkbox"
            aria-label={`select ${describe(variant)}`}
            checked={picked}
            onChange={() => toggleSelected(variant.at)}
          />
        )}
      </td>
      <th scope="row" class="cell-key">
        <span class="name">
          <Icon
            name={row.class === "secret" ? "lock" : row.class === "sensitive" ? "shield" : "key"}
          />
          <span class="key">{row.key}</span>
        </span>
        {flat && (
          <button type="button" class="chip chip-link" onClick={() => go(variant.at.folder)}>
            <Icon name={variant.at.folder === "" ? "home" : "folder"} />
            {folderName(variant.at.folder)}
          </button>
        )}
        {row.class !== "plain" && <span class="chip">{row.class}</span>}
        {(variant.owed || line.needed) && (
          <span class="chip" data-tone="owed">
            {line.needed ? "deploy needs this" : "required"}
          </span>
        )}
        {row.scope && row.scope.length > 0 && (
          <span class="chip" title={`only ${names(row.scope)} read it`}>
            scoped
          </span>
        )}
      </th>
      <td class="cell-value">
        {open ? (
          <Value line={line} />
        ) : (
          <span class="muted">
            only in{" "}
            {(row.scope ?? []).map((where) => (
              <button type="button" class="chip chip-link" key={where} onClick={() => go(where)}>
                <Icon name="folder" />
                {where.slice(1)}
              </button>
            ))}
          </span>
        )}
      </td>
      <td class="cell-tools">{open && <Actions line={line} />}</td>
    </tr>
  );
}

function Value({ line }: { line: KeyLine }) {
  const { variant } = line;
  const key = addressKey(variant.at);
  const revealed = baselines.value.has(key);
  if (variant.reference) {
    return <Linked variant={variant} reference={variant.reference} />;
  }
  const draft = drafts.value.get(key) ?? baselineOf(variant.at, baselines.value);
  const dirty = isDirty(variant.at, drafts.value, baselines.value);
  const problem = problems.value.get(key);
  const unreadable = revealErrors.value.get(key);
  const secret = variant.class === "secret";
  const stamp = variant.set ? `set · v${variant.version}` : "not set";
  return (
    <div class="value" data-dirty={dirty}>
      <div class="value-line">
        <input
          type="text"
          class="value-input"
          value={draft}
          autocomplete="off"
          spellcheck={false}
          disabled={variant.orphaned}
          title={stamp}
          placeholder={
            !variant.set
              ? line.inherits === "root"
                ? "inherits the root value"
                : line.inherits === "base"
                  ? "inherits the base value"
                  : variant.owed || line.needed
                    ? "required"
                    : "not set"
              : secret
                ? "overwrite the secret"
                : revealed
                  ? ""
                  : "••••••••"
          }
          aria-label={`value of ${describe(variant)}`}
          onInput={(event) => setDraft(variant.at, event.currentTarget.value)}
        />
        {dirty && <span class="chip chip-ink">unsaved</span>}
        {problem && (
          <span class="chip" data-tone="owed">
            {problem.kind === "conflict" ? "conflict" : "failed"}
          </span>
        )}
        {!dirty && line.inherits === "root" && <span class="chip">inherits root</span>}
        {!dirty && line.inherits === "base" && <span class="chip">inherits base</span>}
        {variant.kind === "environment" && variant.set && !variant.orphaned && (
          <span class="chip chip-env">
            <Icon name="environment" />
            override
          </span>
        )}
        {variant.orphaned && (
          <span class="chip" data-tone="owed">
            <Icon name="warning" />
            orphaned
          </span>
        )}
        {variant.at.environment === "" && line.overrides.length > 0 && (
          <span
            class="chip chip-env"
            data-tone={line.orphaned ? "owed" : undefined}
            title={`overridden in ${names(line.overrides)}${line.orphaned ? "; an override names an environment that no longer exists" : ""}`}
          >
            <Icon name="environment" />
            {line.overrides.length === 1 ? line.overrides[0] : plural(line.overrides.length, "override")}
          </span>
        )}
      </div>
      {problem && (
        <span class="fault">
          <Icon name="warning" />
          {problem.kind === "conflict"
            ? `Changed underneath you — now ${stored(variant, revealed ? baselines.value.get(key) : undefined)}. Nothing was written; decide again.`
            : problem.message}
        </span>
      )}
      {unreadable && (
        <span class="fault">
          <Icon name="warning" />
          could not reveal: {unreadable}
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

function Linked({ variant, reference }: { variant: Variant; reference: Reference }) {
  const key = addressKey(variant.at);
  const value = baselines.value.get(key);
  const unreadable = revealErrors.value.get(key);
  return (
    <div class="value reference">
      <div class="value-line">
        <span class="chip chip-ink" title={`set · v${variant.version}`}>
          <Icon name="link" />
          reads {referenceLine(reference)}
        </span>
        {value !== undefined ? (
          <span class="resolved">{value}</span>
        ) : (
          <span class="muted">••••••••</span>
        )}
      </div>
      {unreadable && (
        <span class="fault">
          <Icon name="warning" />
          source unreadable: {unreadable}
        </span>
      )}
    </div>
  );
}

function stored(variant: Variant, value: string | undefined): string {
  if (variant.class === "secret") return `v${variant.version} (a secret stays out of the browser)`;
  if (value === undefined) return `v${variant.version}`;
  return `v${variant.version}: ${value}`;
}

function Actions({ line }: { line: KeyLine }) {
  const { variant } = line;
  const current = state.value!;
  const busy = saving.value;
  const revealed = baselines.value.has(addressKey(variant.at));
  const env = variant.at.environment;
  const removal =
    variant.orphaned
      ? `Remove the ${env} override — ${env} no longer exists`
      : env === ""
        ? "Remove value"
        : `Remove the ${env} override`;
  const sections: MenuSection[] = [
    {
      label: "Insights",
      items: [
        { label: "Details and history", icon: "history", onSelect: () => openDrawer(variant.at) },
      ],
    },
    {
      label: "Manage",
      items: [
        ...overrideOptions(current, catalogue.value, variant.at).map((name) => ({
          label: `Override for ${name}`,
          icon: "environment" as const,
          onSelect: () => addOverride({ ...variant.at, environment: name }),
        })),
        ...(revealable(variant)
          ? [{ label: "Copy value", icon: "copy" as const, onSelect: () => void copyValue(variant.at) }]
          : []),
        ...(variant.extra
          ? [{ label: "Dismiss this empty row", icon: "x" as const, onSelect: () => dismiss(variant.at) }]
          : []),
      ],
    },
    {
      items:
        variant.set && !variant.reference
          ? [
              {
                label: removal,
                icon: "trash" as const,
                danger: true,
                disabled: busy,
                onSelect: () => askRemoval([variant.at]),
              },
            ]
          : [],
    },
  ];
  return (
    <span class="cluster">
      {revealable(variant) && (
        <button
          type="button"
          class="iconbtn"
          aria-pressed={revealed}
          title={revealed ? "Hide the value" : "Reveal the value"}
          aria-label={`${revealed ? "hide" : "reveal"} the value of ${describe(variant)}`}
          onClick={() => (revealed ? hide([variant.at]) : void reveal([variant.at]))}
        >
          <Icon name={revealed ? "eyeOff" : "eye"} />
        </button>
      )}
      <button
        type="button"
        class="iconbtn"
        aria-haspopup="dialog"
        title="Details and history"
        aria-label={`details and history of ${describe(variant)}`}
        onClick={() => openDrawer(variant.at)}
      >
        <Icon name="info" />
      </button>
      <Menu label={`actions for ${describe(variant)}`} sections={sections} />
    </span>
  );
}

