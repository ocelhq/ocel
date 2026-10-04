# Research: JSON Schema from the CLI's proto messages

Ticket: [#1631](https://github.com/ocelhq/ocel/issues/1631), part of map [#1627](https://github.com/ocelhq/ocel/issues/1627).
Throwaway branch `research/proto-jsonschema`, cut from `main` at `28e8bf4f6`. Never merged.
Researched 2026-10-04.

The prototype lives in [`research/proto-jsonschema/`](proto-jsonschema/). Its results are in
[`results/formats-asserted.md`](proto-jsonschema/results/formats-asserted.md),
[`results/formats-annotation.md`](proto-jsonschema/results/formats-annotation.md) and
[`results/re2-patterns-unrewritten.md`](proto-jsonschema/results/re2-patterns-unrewritten.md).

## Answer

Use **`bufbuild/protoschema-plugins` `protoc-gen-jsonschema` v0.6.0** with `target=json-bundle`
(the lenient target). It is the only candidate whose schema accepts every real NDJSON line the CLI
writes and still rejects a misspelled field. It validates exactly what protojson *parses*: the
lowerCamel names, plus the original proto names as aliases, int64 as a string or a number, enums
as a name or a number. That is a superset of what the CLI emits. It has four gaps, none of which
blocks the run-event stream today:

1. A oneof does not enforce at-most-one member. Two members set at once is accepted (open
   upstream: [protoschema-plugins#109](https://github.com/bufbuild/protoschema-plugins/issues/109)).
2. `google.protobuf.Duration` is emitted as `format: duration`, which JSON Schema defines as
   ISO 8601 (`PT1S`). protojson writes `1.500s`. A validator that asserts formats rejects real
   output. No message in the stream has a Duration.
3. buf.validate `pattern`s are copied as they are. They are RE2, and JSON Schema patterns are
   ECMA-262. The `[:cntrl:]` patterns in `proto/cli/stream/v1/stream.proto` make the generated
   schema fail to compile in ajv. The fix belongs in the protos, not the plugin.
4. The `json-strict` target is not usable with protojson at all. It types int64 as a JSON
   integer and requires every field with an implicit default.

Wire it as a new template, `proto/buf.gen.schema.yaml`, with the remote plugin
`buf.build/bufbuild/protoschema-jsonschema:v0.6.0`. `scripts/schema/build.mjs` runs it into a
temporary directory and writes the bundles under `www/public/schema/<VERSION>/cli/`, next to
`ocel.schema.json`. The existing Generated job (`scripts/check/derived.sh`) then covers drift with
no change to the job itself.

Envelopes should **not** carry a `schema` field. Leave discovery to `ocel schema` and
`ocel help --json` (reasons [below](#envelope-schema-field)).

## What protojson requires

From the [ProtoJSON spec](https://protobuf.dev/programming-guides/json/):

- int64, fixed64 and uint64: "JSON value will be a decimal string. Either numbers or strings are
  accepted."
- enum: "The name of the enum value as specified in proto is used. Parsers accept both enum names
  and integer values."
- bytes: "standard base64 encoding with paddings".
- Duration: "Generated output always contains 0, 3, 6, or 9 fractional digits, followed by the
  suffix 's'."
- Timestamp: "Uses RFC 3339 … Z-normalized with 0, 3, 6 or 9 fractional digits."
- Any: `{"@type": "url", "f": v, …}`. Struct: any JSON object. Wrappers: the wrapped primitive's
  representation, "except that null is allowed". Empty: `{}`.
- Field names: "Parsers accept both the lowerCamelCase name (or the one specified by the json_name
  option) and the original proto field name."
- Presence: a field without presence that holds its default is omitted unless the serializer is
  told to emit it.

So a schema for protojson *output* must leave every implicit-presence field optional, and type
int64 as a string. A schema for protojson *input* must also accept proto names, numbers for int64,
and enum numbers.

The CLI writes protojson with default options. The sink is
`cli/internal/terminal/jsonlines.go:36-76`: `protojson.Marshal` plus an `operation` object whose
`time`, `level`, `phase`, `subject` and `message` are always present, empty or not. Those forced
fields are still valid protojson.

What JSON Schema says about the edges:

- Patterns "SHOULD be valid according to the regular expression dialect described in ECMA-262"
  ([2020-12 core §6.4](https://json-schema.org/draft/2020-12/json-schema-core#section-6.4)).
- `format` is an annotation by default
  ([validation §7.2.1](https://json-schema.org/draft/2020-12/json-schema-validation#section-7.2.1)),
  and `duration` is "from the ISO 8601 ABNF as given in Appendix A of RFC 3339"
  ([§7.3.1](https://json-schema.org/draft/2020-12/json-schema-validation#section-7.3.1)).
- RE2 defines `[[:cntrl:]]` as `[\x00-\x1F\x7F]` and `[[:space:]]` as `[\t\n\v\f\r ]`
  ([RE2 syntax](https://github.com/google/re2/wiki/Syntax)).

## Candidates

Maintenance data is from the GitHub API on 2026-10-04. Remote-plugin data is from
[`bufbuild/plugins` at `19c87a9`](https://github.com/bufbuild/plugins/tree/19c87a9a9305a0e85b8b62a4365936c19a81c705/plugins),
the repo buf.build's remote plugins are built from.

| | protoschema-plugins | sudorandom connect-openapi | pubg | chrusty |
|---|---|---|---|---|
| Version examined | [v0.6.0](https://github.com/bufbuild/protoschema-plugins/releases/tag/v0.6.0) (2026-04-28), `2ae5aad` | [v0.28.0](https://github.com/sudorandom/protoc-gen-connect-openapi/releases/tag/v0.28.0) (2026-09-30), `ea12c19` | [v0.8.0](https://github.com/pubg/protoc-gen-jsonschema/releases/tag/v0.8.0) (2025-08-20), `e4fba96` | [1.4.1](https://github.com/chrusty/protoc-gen-jsonschema/releases/tag/1.4.1) (2023-04-18), `956cc32` |
| Activity | last commit 2026-08-18; Buf-owned; README says "alpha" | last commit 2026-09-30; releases every few weeks | last commit 2025-08-20 | **archived** (`archived: true`); last commit 2024-02-12 |
| Draft emitted | 2020-12 ([README](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/README.md)) | 2020-12, from `format=jsonschema`, added in [v0.27.0](https://github.com/sudorandom/protoc-gen-connect-openapi/releases/tag/v0.27.0) ([source](https://github.com/sudorandom/protoc-gen-connect-openapi/blob/ea12c19a9abc32f3c1f22267bdaf27327ebbc3d5/internal/converter/jsonschema.go#L19-L23)) | 04 to 2020-12, option `draft` | draft-04 (and 06 for some files; see its testdata) |
| Layout | one file per message, named `<full.Name>.jsonschema[.strict][.bundle].json`, `$id` = file name; `-bundle` inlines dependencies under `$defs` with a root `$ref` ([source](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L158-L225)) | one document per proto file, or one merged one with `path=`; every type under `$defs`; no root `$ref` and no `$id` | one file per proto file; root `$ref` to `entrypoint_message` | one file per message under `<package>/` |
| Names | `json` targets: lowerCamel properties, proto names as `patternProperties` aliases ([L311-L343](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L311-L343)); `-strict` drops the aliases | lowerCamel only, or proto names only (`with-proto-names`) | lowerCamel, or proto names only | `json_fieldnames` or `proto_and_json_fieldnames` |
| int64 / uint64 | lenient: integer or `^-?[0-9]+$` string ([L780-L799](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L780-L799)); **strict: integer only**, which rejects protojson output | `type: string, format: int64`, with no digit pattern ([schema.go L221-L226](https://github.com/sudorandom/protoc-gen-connect-openapi/blob/ea12c19a9abc32f3c1f22267bdaf27327ebbc3d5/internal/converter/schema/schema.go#L221-L226)) | string with `respect_protojson_int64`; the default is integer. **Map values are broken**: the map node itself gets `type: string` ([L212-L219](https://github.com/pubg/protoc-gen-jsonschema/blob/e4fba968090b860bb32fd69ab906518f0ad60ddb/pkg/modules/1_frontend_generator.go#L212-L219)) | string unless `disallow_bigints_as_strings` |
| Enums | lenient: names plus int32; strict: names only ([L515-L610](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L515-L610)) | names; numbers with `include-number-enum-values` | names | names, or names plus numbers |
| Oneof | **not enforced** ([#109](https://github.com/bufbuild/protoschema-plugins/issues/109)) | at most one, via `anyOf` plus `not`, and `unevaluatedProperties: false` ([L257-L300](https://github.com/sudorandom/protoc-gen-connect-openapi/blob/ea12c19a9abc32f3c1f22267bdaf27327ebbc3d5/internal/converter/schema/schema.go#L257-L300)) | at most one, via `oneOf` ([L46-L60](https://github.com/pubg/protoc-gen-jsonschema/blob/e4fba968090b860bb32fd69ab906518f0ad60ddb/pkg/modules/1_frontend_generator.go#L46-L60)) | `enforce_oneof`, but it requires a member |
| Well-known types | Timestamp `date-time`; Duration `format: duration` (ISO 8601, wrong for protojson); FieldMask string; Struct object; Value any; Any object with `@type`; wrappers as the primitive; Empty an empty closed object ([L1400-L1450](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L1400-L1450)) | all of them special-cased ([well_known.go](https://github.com/sudorandom/protoc-gen-connect-openapi/blob/ea12c19a9abc32f3c1f22267bdaf27327ebbc3d5/internal/converter/util/well_known.go#L12-L35)); Duration has the same `format: duration`; Any declares `type` and `value`, not `@type`; wrappers do not accept null | only Timestamp, Duration, Any and NullValue ([L331-L401](https://github.com/pubg/protoc-gen-jsonschema/blob/e4fba968090b860bb32fd69ab906518f0ad60ddb/pkg/modules/1_frontend_generator.go#L331-L401)); wrappers, Struct, Value and FieldMask render as plain messages | a subset |
| proto3 `optional` | optional; absence allowed | optional, plus `null` | `oneOf [null, X]`, and **required** under `respect_protojson_presence` | required in the experiment |
| Required | only from `(buf.validate.field).required` in lenient; strict requires every implicit field and needs EmitUnpopulated ([L75-L95](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L75-L95)) | none | default: every singular non-optional field; with presence: every message field ([L62-L72](https://github.com/pubg/protoc-gen-jsonschema/blob/e4fba968090b860bb32fd69ab906518f0ad60ddb/pkg/modules/1_frontend_generator.go#L62-L72)). Both reject omitted defaults | as the experiment shows |
| bytes | `^[A-Za-z0-9+/]*={0,2}$`; buf.validate `len` is converted to base64 length ([L1336-L1373](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L1336-L1373)) | `format: byte`; **`bytes.len` is applied raw as `maxLength`**, so the 12-character base64 of an 8-byte span id fails ([protovalidate/schema.go L1022-L1025](https://github.com/sudorandom/protoc-gen-connect-openapi/blob/ea12c19a9abc32f3c1f22267bdaf27327ebbc3d5/internal/converter/protovalidate/schema.go#L1022-L1025)) | strict padded base64 pattern | pattern |
| additionalProperties | `false` by default; option `additional_properties=true` | `false`, or `unevaluatedProperties: false` when branches add properties | `additional_properties=AlwaysFalse` | `disallow_additional_properties` |
| buf.validate | yes: numeric bounds, string len, pattern, well-known formats, enum in/const, bytes len, required | yes, feature `protovalidate` (on by default) | no; uses its own `pubg.jsonschema` options | no; uses its own options |
| Remote plugin | [`buf.build/bufbuild/protoschema-jsonschema`](https://github.com/bufbuild/plugins/tree/19c87a9a9305a0e85b8b62a4365936c19a81c705/plugins/bufbuild/protoschema-jsonschema) v0.5.0 to v0.6.0; **verified**: output is byte-identical to the local build | [`buf.build/community/sudorandom-connect-openapi`](https://github.com/bufbuild/plugins/tree/19c87a9a9305a0e85b8b62a4365936c19a81c705/plugins/community/sudorandom-connect-openapi) up to v0.28.0 | **none** | [`buf.build/community/chrusty-jsonschema`](https://github.com/bufbuild/plugins/tree/19c87a9a9305a0e85b8b62a4365936c19a81c705/plugins/community/chrusty-jsonschema) up to v1.4.1 (used in the experiment) |
| Go tool | `go get -tool github.com/bufbuild/protoschema-plugins/cmd/protoc-gen-jsonschema@v0.6.0` works in `cli/`, but it **upgrades** `buf.build/go/protovalidate` v1.0.0 to v1.2.0 and `cel-go` v0.26.1 to v0.28.0 in the CLI's runtime module graph (reproduced, then reverted) | `go run …@v0.28.0` works | `go run …@v0.8.0` works | n/a |

Also considered and dropped:

- **google/gnostic `protoc-gen-openapi`**: emits OpenAPI 3.0.3
  ([generator.go#L104](https://github.com/google/gnostic/blob/baf14b9600b0/cmd/protoc-gen-openapi/generator/generator.go#L104)),
  whose schema object is not JSON Schema 2020-12. Last release
  [v0.7.0](https://github.com/google/gnostic/releases/tag/v0.7.0) was in 2023.
- **grpc-gateway `protoc-gen-openapiv2`**: emits Swagger 2.0
  ([template.go#L2324](https://github.com/grpc-ecosystem/grpc-gateway/blob/79d1673a7d1f/protoc-gen-openapiv2/internal/genopenapi/template.go#L2324)),
  a draft-04 subset, and only for messages that services reference.

## Prototype

Steps, from the repo root on this branch:

1. `research/proto-jsonschema/capture.sh` builds `ocel` from this checkout (cli at
   `28e8bf4f6`). It runs `ocel deploy --log-format json --yes` on a copy of
   `tests/fixtures/deploy/go`, with its VPS config, `OCEL_VPS_HOST=127.0.0.1` and an empty
   `OCEL_PROVIDERS_DIR`. No cloud credentials are involved. The run fails at the check phase and
   writes three real lines to
   [`samples/deploy-vps.ndjson`](proto-jsonschema/samples/deploy-vps.ndjson). The last is the
   summary, with `durationMs: "16"` (int64 as a string), `LEVEL_ERROR` (an enum name), an RFC 3339
   `time`, a base64 `spanId`, and the `started`/`ended`/`summary` oneof members.
2. `OCEL_RESEARCH_SAMPLE_OUT=… go test ./cli/internal/terminal/ -run TestResearchWritesPopulatedSummarySample`
   writes [`samples/sink-populated.ndjson`](proto-jsonschema/samples/sink-populated.ndjson)
   through the real `JSONLines` sink. It covers a summary with `tier`, `apps`, `origin`,
   `propagation`, `urlNotes`, `promotionId` and a `durationMs` above 2^53. The test file is
   `cli/internal/terminal/research_sample_test.go`.
3. `research/proto-jsonschema/probe/` holds a synthetic `probe.v1.Probe`. It has int64, uint64,
   bytes, an enum, proto3 `optional`, maps, repeated fields, a oneof, Timestamp, Duration, Struct,
   Value, Any, Int64Value, StringValue, Empty and FieldMask. `go run .` turns
   [`probe/input.json`](proto-jsonschema/probe/input.json) (parse-only forms) into protojson's
   canonical output, [`samples/probe.json`](proto-jsonschema/samples/probe.json).
4. `research/proto-jsonschema/templates/gen.sh` generates schemas into `gen/` with each plugin,
   for `proto/cli/stream` and for the probe.
5. `VALIDATOR_DIR=<dir with ajv@8.17.1 ajv-formats@3.0.1 ajv-draft-04@1.0.0> node research/proto-jsonschema/validate.mjs`
   validates every sample and mutation and prints a table. `FORMATS=annotate` turns off format
   assertion. `REWRITE=0` leaves the RE2 classes in the patterns.

### Results for the stream (formats asserted)

`emit` means protojson writes the line, so a schema must accept it. `reject` means
`protojson.Unmarshal` refuses it. `parse` means protojson accepts it but never writes it. ✗ marks
a schema that disagrees with protojson.

| case | protojson | **protoschema json** | protoschema json-strict | connect-openapi | connect-openapi, protovalidate off | pubg (int64+presence) | pubg (int64) | chrusty |
|---|---|---|---|---|---|---|---|---|
| real deploy lines 1–2 | emit | **accept** | reject ✗ | reject ✗ | accept | accept | reject ✗ | reject ✗ |
| real deploy line 3 (summary) | emit | **accept** | reject ✗ | accept | accept | reject ✗ | reject ✗ | reject ✗ |
| populated summary (sink) | emit | **accept** | reject ✗ | accept | accept | reject ✗ | reject ✗ | reject ✗ |
| `durationMS` (misspelled) | reject | **reject** | reject | reject | reject | reject | reject | reject |
| `headLine` (misspelled) | reject | **reject** | reject | reject | reject | reject | reject | reject |
| `origin.vendr` (nested misspelling) | reject | **reject** | reject | reject | reject | reject | reject | reject |
| `tier: "TIER_BOGUS"` | reject | **reject** | reject | reject | reject | reject | reject | reject |
| `durationMs: "16ms"` | reject | **reject** | reject | accept ✗ | accept ✗ | reject | reject | reject |
| `summary` and `resumed` both set | reject | **accept ✗** | reject | reject | reject | reject | reject | reject |
| `time: "yesterday"` | reject | **reject** | reject | reject | reject | reject | reject | reject |
| `spanId: "!!!!…"` | reject | **reject** | reject | reject | accept ✗ | reject | reject | reject |
| `durationMs: 16` (number) | parse | accept | reject | reject | reject | reject | reject | reject |
| `tier: 2` (enum number) | parse | accept | reject | reject | reject | reject | reject | reject |
| `duration_ms` (proto name) | parse | accept | reject | reject | reject | reject | reject | reject |

pubg and chrusty reject the misspellings only because they also demand fields protojson omits.
They reject the correct lines for the same reason. With `REWRITE=0`, protoschema and
connect-openapi do not compile at all:
`Invalid regular expression: /^(/[^/#[:cntrl:]]+)*$/u: Lone quantifier brackets`, from
`MissingVariable.folder` in `proto/cli/stream/v1/stream.proto`.

The reject for the recommended plugin is
`/summary must NOT have additional properties (durationMS)`, from `additionalProperties: false`.
The plugin sets that by default
([L301](https://github.com/bufbuild/protoschema-plugins/blob/2ae5aad82d506caf3c8dd8c70ed4fdb55992710a/internal/protoschema/jsonschema/jsonschema.go#L301));
the proto-name aliases are carried in `patternProperties`, so they do not open the object.

### Results for the probe

With formats asserted, both protoschema and connect-openapi reject protojson's own `"took":"1.500s"`
(`/took must match format "duration"`), and both accept `"PT1S"`, which protojson rejects. With
`FORMATS=annotate` (the 2020-12 default), protoschema json and connect-openapi accept the full
canonical output. The other gaps:

- connect-openapi accepts `counts: {"a": "x"}` for a `map<string, int64>`, because int64 strings
  have no pattern.
- pubg, even with `required` dropped, rejects
  - `counts` (`/counts must be string`, the map bug);
  - `meta` (Struct);
  - `anyValue: null` (Value);
  - `wrappedBig`, `wrappedStr` and `mask` (`must be object`).
- protoschema json-strict rejects every row, because int64 is typed as a JSON integer
  (`/big must be integer`).

The full tables, with the first errors for every rejection, are in
[`results/`](proto-jsonschema/results/).

## Recommendation

### Plugin

`bufbuild/protoschema-plugins` `protoc-gen-jsonschema` **v0.6.0**, option `target=json-bundle`.
Leave `additional_properties` unset; the default is `false`, which closes every object.

Why not the others:

- **connect-openapi** comes closest to output-exact: names only, enums as names, oneofs enforced.
  But with protovalidate on, it rejects every real line that has a `spanId` (`bytes.len`
  bug). With protovalidate off, it accepts non-numeric int64 strings and non-base64 bytes.
  Its single per-file `$defs` document also has no root and no `$id` to publish.
- **pubg** has no remote plugin. It marks omitted fields required, mis-types maps with integer
  values, and renders wrappers, Struct, Value and FieldMask as messages.
- **chrusty** is archived and emits draft-04.

Lenient, not strict: the published schema describes what an `ocel` consumer can feed back into a
protojson parser. That is also what the stream's stability clause promises. Emitted lines are a
subset of it.

### Template

Add a new **`proto/buf.gen.schema.yaml`**. It cannot join `buf.gen.yaml`, which excludes
`proto/cli/stream` and owns `clean: true` SDK output dirs, or `buf.gen.go.yaml`, whose input is
all of `proto/`:

```yaml
version: v2
clean: true
inputs:
  - directory: proto
    paths:
      - proto/cli/stream
      # plus the CLI's result-message package when the map adds it
plugins:
  - remote: buf.build/bufbuild/protoschema-jsonschema:v0.6.0
    out: schema
    opt:
      - target=json-bundle
```

Use the remote plugin, not a Go tool. As a tool in `cli/go.mod` (the only module with a `tool`
block), it drags protovalidate v1.2.0 and cel-go v0.28.0 into the CLI binary's dependency graph.
That would change the CLI's runtime to get a generator. `pnpm gen` already depends on buf.build
remote plugins (py, connect-py, buffa, connect-rust), so this adds no new failure mode. The pinned
version and the drift diff on the output keep it reviewable.

### Where the output lands

`scripts/schema/build.mjs` already knows `VERSION` and writes
`www/public/schema/${VERSION}/ocel.schema.json`. Extend it to:

1. run `buf generate --template proto/buf.gen.schema.yaml --output <mkdtemp>` (`--output`
   prefixes the template's `out`);
2. copy each `<full.Name>.jsonschema.bundle.json` to
   **`www/public/schema/${VERSION}/cli/<full.Name>.schema.json`**, for example
   `www/public/schema/0.0.1/cli/cli.stream.v1.RunEvent.schema.json` and
   `…/cli/cli.stream.v1.RunSummary.schema.json`;
3. rewrite the bundle's relative `$id` (`cli.stream.v1.RunEvent.jsonschema.bundle.json`) to its
   absolute URL, `https://ocel.dev/schema/${VERSION}/cli/cli.stream.v1.RunEvent.schema.json`. The
   `$defs` keys and internal `#/$defs/…` refs stay as they are;
4. fail the build if any `pattern` in the output does not compile as `new RegExp(p, "u")`. That
   guards against the RE2-only syntax the protos use today.

Names come mechanically from the proto full name, so there is no hand-kept table. Every message in
the input packages gets a bundle, so the summary alone can be validated as well as a whole line.

Versioning:

- The directory follows `VERSION`, as the config schema does.
- `scripts/release/version.mjs` stamps schema URLs only through
  `SCHEMA_URL = /https:\/\/ocel\.dev\/schema\/[0-9A-Za-z.+-]+\/ocel\.schema\.json/g` (line 15).
  It must widen to any path under `/schema/<version>/`, or references to the new files will
  not be stamped on release.
- The URL base should come from the same docs origin as `docs_url`, as the map's locked item 4
  says. Today `cli/internal/commands/projectinit/init.go:199-201` hardcodes it.

### Drift check

There is no new job. The Generated job (`.github/workflows/lint.yml:225-243`) runs
`scripts/check/derived.sh`, which runs `node scripts/schema/build.mjs` and diffs everything listed
under `generated` in `scripts/check/derived.json`. `www/public/schema` is already in `generated`.
`proto/` and `scripts/schema/` are already in `sources`, so `mise run check` (`check.mjs:149`)
selects it on a proto change. The only edit is to `build.mjs`. The output is deterministic: two
consecutive generations diffed clean, and `json.MarshalIndent` sorts keys.

### Proto prerequisite

Rewrite the RE2 POSIX classes in buf.validate `pattern`s to their RE2-documented ASCII ranges,
which are also valid ECMA-262:

- `[:cntrl:]` becomes `\x00-\x1f\x7f`;
- `[:space:]` becomes ` \t\n\v\f\r`.

They appear in `proto/cli/stream/v1/stream.proto:58,64,68`,
`proto/app/resources/v1/variables.proto:21,30,45,49,67,69,86` and
`proto/provider/contract/v1/contract.proto:224,226`. RE2 semantics are unchanged. CEL
`matches('[[:cntrl:]]')` expressions are not copied into the schema and can stay. This is a
contract-path change. The prototype's `REWRITE` step does exactly this substitution and shows the
schemas then compile and validate.

### Envelope `schema` field

**Leave discovery to `ocel schema` / `ocel help --json`; envelopes carry no `schema` field.**

1. The schema is a function of the binary's version and the command. Both are known before the
   command runs, so a program can ask once (`ocel schema <command>`) instead of reading a URL off
   every document.
2. A URL built from the binary's version 404s for nightly, rc and dev builds. `build.mjs` only
   writes a directory for `VERSION`, and a nightly reports `X.Y.Z-0.nightly.…`. `ocel schema`
   can embed the same bundles and work offline for every build.
3. Run commands print NDJSON. A per-document field would have to go on every line or only on the
   summary, and both break the symmetry between the two shapes that locked item 2 sets up.
4. JSON Schema links instances to schemas out of band. It recommends the `describedby` link
   relation
   ([2020-12 core §9.5.1](https://json-schema.org/draft/2020-12/json-schema-core#section-9.5.1)),
   not a property in the instance.
5. The locked envelope is `{"ok", "data" | "error"}`. A new top-level field is a contract change
   that every consumer has to carry.

`ocel help --json` should name each command's result message and its schema URL. `ocel schema`
should print the bundle.

## Follow-ups observed

- Patterns in `proto/` use RE2-only POSIX classes that JSON Schema (ECMA-262) cannot compile.
  Locations are listed above. Fix them before or together with the wiring.
- After a release, `VERSION` stays at the released version, so `build.mjs` regenerates the
  released directory in place on `main`. `www/public/schema/0.0.1/ocel.schema.json` was created
  by `2fef418c8 chore(release): v0.0.1` and will change on the next schema-affecting commit; the
  same happened to `0.0.0` in `1dfd146eb`. Published, versioned URLs then describe unreleased
  behaviour. Decide whether a released directory is frozen and `main` writes to the next version.
- `SCHEMA_URL` in `scripts/release/version.mjs:15` only matches `ocel.schema.json`.
- Upstream: protoschema-plugins `format: duration` for `google.protobuf.Duration` contradicts
  protojson. `json-strict` types int64 as an integer, which contradicts protojson output. The
  oneof gap is [#109](https://github.com/bufbuild/protoschema-plugins/issues/109).
- Upstream: connect-openapi applies buf.validate `bytes.len` to the base64 string length.
