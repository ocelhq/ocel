import type { z } from "zod";

export type MaybePromise<T> = T | Promise<T>;

/** Terminal-or-pending upload state as surfaced to the client by op=poll. */
export type UploadStatusState = "pending" | "succeeded" | "expired";

/**
 * The minimal request surface the blob route and uploader middleware rely on.
 * A Web Fetch `Request` (and a Next `NextRequest`) satisfy it structurally, so
 * the core stays framework-agnostic; framework bindings only narrow the type.
 */
export interface BlobRequest {
  readonly url: string;
  readonly headers: { get(name: string): string | null };
  json(): Promise<unknown>;
}

export interface FileInfo {
  name: string;
  size: number;
  mimeType: string;
}

export interface CompletedFile {
  key: string;
  name: string;
  size: number;
  mimeType: string;
  /** The real object location; equal to key. */
  path: string;
}

export type ParsedInput<TInput> = TInput extends z.ZodType
  ? z.infer<TInput>
  : undefined;

export type LimitValue<TMetadata, T> =
  | T
  | ((ctx: { metadata: TMetadata }) => T);

export interface Limits<TMetadata> {
  maxFileSize?: LimitValue<TMetadata, number>;
  maxFileCount?: LimitValue<TMetadata, number>;
  minFileCount?: LimitValue<TMetadata, number>;
}

export interface StructuredPath {
  prefix?: string;
  randomSuffix?: boolean;
}

export type PathFn<TMetadata> = (ctx: {
  file: FileInfo;
  metadata: TMetadata;
}) => string;

export type PathConfig<TMetadata> = StructuredPath | PathFn<TMetadata>;

export interface UploaderAuth<
  TReq,
  TInput extends z.ZodType | undefined,
  TMetadata,
> {
  input?: TInput;
  middleware: (ctx: {
    req: TReq;
    input: ParsedInput<TInput>;
  }) => MaybePromise<TMetadata>;
}

export interface UploaderUpload<TMetadata> {
  accept?: string[];
  limits?: Limits<TMetadata>;
  path?: PathConfig<TMetadata>;
  contentDisposition?: string;
  onUploadComplete?: (ctx: {
    metadata: TMetadata;
    file: CompletedFile;
  }) => MaybePromise<void>;
}

/** A built uploader: runtime config plus phantom types the client recovers from `typeof bucket`. */
export interface Uploader<
  TInputParsed = unknown,
  TMetadata = unknown,
  TReq = BlobRequest,
> {
  readonly auth: UploaderAuth<TReq, z.ZodType | undefined, TMetadata>;
  readonly upload: UploaderUpload<TMetadata>;
  /** phantom, type-only: the parsed input this uploader accepts */
  readonly __input?: TInputParsed;
}

export type AnyUploader = Uploader<any, any, any>;
