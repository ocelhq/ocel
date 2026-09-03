import {
  cellOf,
  folderName,
  socketState,
  type MatrixCell,
  type MatrixRow,
  type State,
} from "../model";
import { address, hoveredApp, selected } from "../store";

export function Matrix({ current }: { current: State }) {
  return (
    <div class="scroller">
      <table class="matrix" data-threading={hoveredApp.value !== null}>
        <thead>
          <tr>
            <th class="corner">variable</th>
            {current.matrix.columns.map((column) => (
              <th class="column" key={column}>
                {folderName(column)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {current.matrix.rows.map((row) => (
            <tr key={row.key}>
              <th>
                <span class="key">{row.key}</span>
                <span class="class">
                  {row.scope?.length
                    ? `${row.class} · scoped to ${row.scope.join(" and ")}`
                    : row.class}
                </span>
              </th>
              {current.matrix.columns.map((column) => {
                const cell = cellOf(row, column);
                return (
                  <td key={column}>
                    {cell && <Socket row={row} cell={cell} />}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Socket({ row, cell }: { row: MatrixRow; cell: MatrixCell }) {
  const where = folderName(cell.folder);
  const app = hoveredApp.value;
  const threaded =
    app !== null && (cell.folder === "" || cell.folder === app.folder);
  const className = `socket${threaded ? " threaded" : ""}`;

  if (cell.state === "forbidden") {
    const title = `${row.key} holds no value in ${where} — nothing would read one`;
    return (
      <span
        class={`${className} forbidden`}
        data-state="forbidden"
        title={title}
        role="img"
        aria-label={title}
      >
        <span class="pip" />
      </span>
    );
  }

  const title = `${row.key} in ${where}: ${cell.state}${cell.set ? ", set" : ", not set"}`;
  const picked = selected.value;
  return (
    <button
      type="button"
      class={className}
      data-state={socketState(cell)}
      title={title}
      aria-label={title}
      aria-pressed={
        picked !== null && picked.key === row.key && picked.folder === cell.folder
      }
      onClick={() =>
        address({ key: row.key, folder: cell.folder, environment: "" })
      }
    >
      <span class="pip" />
    </button>
  );
}
