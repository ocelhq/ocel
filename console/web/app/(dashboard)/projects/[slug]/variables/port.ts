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

function held<T>(answer: Answer<T>): T {
  if (answer.ok) {
    return answer.result;
  }
  throw new VarsError(answer.status, answer.message);
}

export function consolePort(projectId: string, environment: string): VarsPort {
  return {
    read: async () => held(await readState(projectId, environment)),
    reveal: async (cells) => held(await revealValues(projectId, environment, [...cells])),
    set: async (at, value, version) => {
      held(await setValue(projectId, environment, at, value, version));
    },
    remove: async (at, version) => {
      held(await removeValue(projectId, environment, at, version));
    },
    history: async (at) => held(await listVersions(projectId, environment, at)),
    other: async () => held(await otherValues(projectId, environment)),
    copy: async (cells) => held(await copyValues(projectId, environment, [...cells])).results,
  };
}
