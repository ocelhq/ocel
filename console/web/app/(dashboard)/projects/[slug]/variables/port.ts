import type { VariablesPort } from "@ui/variables";
import { VariablesError } from "@ui/variables";

import {
  type Answer,
  copyValues,
  listVersions,
  otherValues,
  readState,
  removeValue,
  revealValues,
  setInEnvSource,
  setValue,
} from "./actions";

function resultOf<T>(answer: Answer<T>): T {
  if (answer.ok) {
    return answer.result;
  }
  throw new VariablesError(answer.status, answer.message);
}

export function consolePort(projectId: string, environment: string): VariablesPort {
  return {
    read: async () => resultOf(await readState(projectId, environment)),
    reveal: async (cells) => resultOf(await revealValues(projectId, environment, [...cells])),
    set: async (at, value, version) => {
      resultOf(await setValue(projectId, environment, at, value, version));
    },
    setInEnvSource: async (at, value) =>
      resultOf(await setInEnvSource(projectId, environment, at, value)),
    remove: async (at, version) => {
      resultOf(await removeValue(projectId, environment, at, version));
    },
    history: async (at) => resultOf(await listVersions(projectId, environment, at)),
    other: async () => resultOf(await otherValues(projectId, environment)),
    copy: async (cells) => resultOf(await copyValues(projectId, environment, [...cells])).results,
  };
}
