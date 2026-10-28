export type AttributeValue = string | number | boolean | string[];

export type Attributes = Record<string, AttributeValue | undefined>;

export interface OpenSpan {
  setAttributes(attributes: Attributes): void;
}

export type InSpan = <T>(
  name: string,
  attributes: Attributes,
  fn: (span: OpenSpan) => Promise<T>,
) => Promise<T>;

export const untraced: InSpan = (_name, _attributes, fn) => fn({ setAttributes() {} });

export interface ModuleImport {
  file: string;
  load: () => Promise<unknown>;
}

export async function importModules(
  imports: ReadonlyArray<ModuleImport>,
  inSpan: InSpan,
): Promise<void> {
  for (const { file, load } of imports) {
    await inSpan("import module", { "code.filepath": file }, load);
  }
}
