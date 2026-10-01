import { declarationSite } from "../../../../src/declaration/callsite.js";

export function siteOfThisFile(): string {
  return declarationSite();
}
