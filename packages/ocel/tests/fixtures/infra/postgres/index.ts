import { declarationSite } from "../../../../src/utils/callsite.js";

export function siteOfThisFile(): string {
  return declarationSite();
}
