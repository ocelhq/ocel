import type { VarsPort } from "@ui/vars";
import { VarsError } from "@ui/vars";

import {
  type Answer,
  copyValues,
  listVersions,
  otherValues,
  readState,
  removeValue,
  revealValues,
  setValue,
} from "./actions";

function resultOf<T>(answer: Answer<T>): T {
  if (answer.ok) {
    return answer.result;
  }
  throw new VarsError(answer.status, answer.message);
}

export function consolePort(projectId: string, environment: string): VarsPort {
  return {
    read: async () => resultOf(await readState(projectId, environment)),
    reveal: async (cells) => resultOf(await revealValues(projectId, environment, [...cells])),
    set: async (at, value, version) => {
      resultOf(await setValue(projectId, environment, at, value, version));
    },
    remove: async (at, version) => {
      resultOf(await removeValue(projectId, environment, at, version));
    },
    history: async (at) => resultOf(await listVersions(projectId, environment, at)),
    other: async () => resultOf(await otherValues(projectId, environment)),
    copy: async (cells) => resultOf(await copyValues(projectId, environment, [...cells])).results,
  };
}
