import { api, query } from "../api";
import {
  addressable,
  cellOf,
  chipTitle,
  coordinateLine,
  doneLabel,
  environmentName,
  held,
  overrideOf,
  owedCount,
  verdictLine,
  type MatrixCell,
  type MatrixRow,
  type State,
} from "../model";
import {
  address,
  draft,
  error,
  leave,
  mutate,
  saving,
  selected,
  versions,
} from "../store";

export function Inspector({ current }: { current: State }) {
  const at = selected.value;
  const row = at && current.matrix.rows.find((candidate) => candidate.key === at.key);
  const cell = row && at ? cellOf(row, at.folder) : undefined;

  if (!at || !row || !cell) {
    return (
      <aside class="inspector">
        <h2>Pick a cell</h2>
        <p class="empty">
          Each socket is one variable in one folder. Hollow sockets are owed;
          hatched sockets hold no value because nothing would read one.
        </p>
        <Legend />
        <Done current={current} />
      </aside>
    );
  }

  const environment = at.environment;
  const here = held(cell, environment);
  const orphaned = overrideOf(cell, environment)?.orphaned === true;
  const busy = saving.value;

  const write = () =>
    mutate(() =>
      api("PUT", "/api/value", {
        ...at,
        value: draft.value,
        version: here.version,
      }),
    );
  const erase = () =>
    mutate(() =>
      api("DELETE", `/api/value?${query(at)}&version=${here.version}`),
    );

  return (
    <aside class="inspector">
      <h2>{row.key}</h2>
      <p class="coordinate">{coordinateLine(row, cell, environment)}</p>
      <Environments current={current} row={row} cell={cell} />
      <p
        class="verdict-line"
        data-tone={
          !here.set && environment === "" && cell.state === "required"
            ? "owed"
            : undefined
        }
      >
        {verdictLine(cell, environment, here.set, orphaned)}
      </p>
      {cell.problem && (
        <p class="problem">The value here fails its schema: {cell.problem}</p>
      )}
      <div class="field">
        <label for="value">{here.set ? "New value" : "Value"}</label>
        <input
          id="value"
          type="text"
          value={draft.value}
          autocomplete="off"
          spellcheck={false}
          placeholder={here.set ? "replace the value that is set" : ""}
          onInput={(event) => (draft.value = event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && draft.value !== "") void write();
          }}
        />
      </div>
      <div class="actions">
        {!orphaned && (
          <button
            type="button"
            class="save"
            disabled={busy || draft.value === ""}
            onClick={() => void write()}
          >
            {busy ? "Saving…" : "Save"}
          </button>
        )}
        {here.set && (
          <button
            type="button"
            class="remove"
            disabled={busy}
            onClick={() => void erase()}
          >
            Remove
          </button>
        )}
      </div>
      {error.value && <p class="problem">{error.value}</p>}
      <p class="eyebrow">History</p>
      {versions.value.length === 0 ? (
        <p class="empty">No versions yet.</p>
      ) : (
        <ul class="history">
          {versions.value.map((version) => (
            <li key={version.version}>
              <span>v{version.version}</span>
              <span>
                {new Date(version.createdAt * 1000).toLocaleString()} ·{" "}
                {version.size} bytes
              </span>
            </li>
          ))}
        </ul>
      )}
      <Done current={current} />
    </aside>
  );
}

function Environments({
  current,
  row,
  cell,
}: {
  current: State;
  row: MatrixRow;
  cell: MatrixCell;
}) {
  const environments = addressable(current, cell);
  if (environments.length === 1) return <div class="environments" />;
  const picked = selected.value;
  return (
    <div class="environments">
      {environments.map((environment) => {
        const override = overrideOf(cell, environment);
        return (
          <button
            type="button"
            class="environment"
            key={environment}
            data-selected={picked?.environment === environment}
            data-set={held(cell, environment).set}
            data-orphaned={override?.orphaned ? "true" : undefined}
            title={chipTitle(row, environment, override)}
            onClick={() =>
              address({ key: row.key, folder: cell.folder, environment })
            }
          >
            {environmentName(environment)}
            {override?.orphaned && <span class="orphan">orphaned</span>}
          </button>
        );
      })}
    </div>
  );
}

const legend = [
  ["held", "set"],
  ["owed", "required, empty"],
  ["free", "optional override"],
  ["forbidden", "nothing would read it"],
] as const;

function Legend() {
  return (
    <div class="legend">
      {legend.map(([state, caption]) => (
        <span key={state}>
          <span
            class={state === "forbidden" ? "socket forbidden" : "socket"}
            data-state={state}
          >
            <span class="pip" />
          </span>
          {caption}
        </span>
      ))}
    </div>
  );
}

function Done({ current }: { current: State }) {
  return (
    <button type="button" class="done" onClick={leave}>
      {doneLabel(owedCount(current))}
    </button>
  );
}
