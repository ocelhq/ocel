formats: asserted (ajv-formats); RE2 classes rewritten: true

# run-event stream (proto/cli/stream/v1)

| case | protojson | protoschema json (lenient) | protoschema json-strict | connect-openapi format=jsonschema | connect-openapi, protovalidate off | pubg (int64+presence) | pubg (int64 only) | chrusty (archived, draft-04) |
|---|---|---|---|---|---|---|---|---|
| real deploy line 1 | emit | accept | reject ✗ | reject ✗ | accept | accept | reject ✗ | reject ✗ |
| real deploy line 2 | emit | accept | reject ✗ | reject ✗ | accept | accept | reject ✗ | reject ✗ |
| real deploy line 3 (summary) | emit | accept | reject ✗ | accept | accept | reject ✗ | reject ✗ | reject ✗ |
| sink line 1 | emit | accept | reject ✗ | reject ✗ | accept | accept | accept | reject ✗ |
| sink line 2 (populated summary) | emit | accept | reject ✗ | accept | accept | reject ✗ | reject ✗ | reject ✗ |
| sink line 3 | emit | accept | reject ✗ | accept | accept | accept | reject ✗ | reject ✗ |
| summary.durationMs misspelled durationMS | reject | reject | reject | reject | reject | reject | reject | reject |
| summary.headline misspelled headLine | reject | reject | reject | reject | reject | reject | reject | reject |
| summary.origin.vendor misspelled vendr | reject | reject | reject | reject | reject | reject | reject | reject |
| summary.tier unknown enum name | reject | reject | reject | reject | reject | reject | reject | reject |
| summary.durationMs not an integer | reject | reject | reject | accept ✗ | accept ✗ | reject | reject | reject |
| oneof cli: summary and resumed both set | reject | accept ✗ | reject | reject | reject | reject | reject | reject |
| operation.time not RFC 3339 | reject | reject | reject | reject | reject | reject | reject | reject |
| operation.spanId not base64 | reject | reject | reject | reject | accept ✗ | reject | reject | reject |
| summary.durationMs as JSON number | parse | accept | reject | reject | reject | reject | reject | reject |
| summary.tier as enum number | parse | accept | reject | reject | reject | reject | reject | reject |
| summary.duration_ms (proto name) | parse | accept | reject | reject | reject | reject | reject | reject |

## first errors per rejection (run-event stream (proto/cli/stream/v1))

- protoschema json-strict / real deploy line 1: /operation/started must have required property 'parentSpanId'
- connect-openapi format=jsonschema / real deploy line 1: /operation/spanId must NOT have more than 8 characters
- pubg (int64 only) / real deploy line 1: /operation/started must be null; /operation/started must have required property 'parentSpanId'; /operation/started must match exactly one schema in oneOf
- chrusty (archived, draft-04) / real deploy line 1: / must have required property 'waiting'; / must have required property 'resumed'; / must have required property 'summary'
- protoschema json-strict / real deploy line 2: /operation/ended must have required property 'title'; /operation/ended/startTimeUnixNano must be integer
- connect-openapi format=jsonschema / real deploy line 2: /operation/spanId must NOT have more than 8 characters
- pubg (int64 only) / real deploy line 2: /operation/ended must be null; /operation/ended must have required property 'title'; /operation/ended must match exactly one schema in oneOf
- chrusty (archived, draft-04) / real deploy line 2: / must have required property 'waiting'; / must have required property 'resumed'; / must have required property 'summary'
- protoschema json-strict / real deploy line 3 (summary): /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'interrupted'
- pubg (int64+presence) / real deploy line 3 (summary): /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / real deploy line 3 (summary): /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / real deploy line 3 (summary): /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json-strict / sink line 1: /operation/ended/startTimeUnixNano must be integer
- connect-openapi format=jsonschema / sink line 1: /operation/spanId must NOT have more than 8 characters
- chrusty (archived, draft-04) / sink line 1: / must have required property 'waiting'; / must have required property 'resumed'; / must have required property 'summary'
- protoschema json-strict / sink line 2 (populated summary): /operation must have required property 'spanId'; /summary must have required property 'detail'; /summary must have required property 'interrupted'
- pubg (int64+presence) / sink line 2 (populated summary): /summary must be null; /summary must have required property 'missing'; /summary must match exactly one schema in oneOf
- pubg (int64 only) / sink line 2 (populated summary): /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'detail'
- chrusty (archived, draft-04) / sink line 2 (populated summary): /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json-strict / sink line 3: /operation must have required property 'spanId'
- pubg (int64 only) / sink line 3: /operation must have required property 'spanId'
- chrusty (archived, draft-04) / sink line 3: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / summary.durationMs misspelled durationMS: /summary must NOT have additional properties (durationMS)
- protoschema json-strict / summary.durationMs misspelled durationMS: /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'durationMs'
- connect-openapi format=jsonschema / summary.durationMs misspelled durationMS: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.durationMs misspelled durationMS: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.durationMs misspelled durationMS: /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / summary.durationMs misspelled durationMS: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / summary.durationMs misspelled durationMS: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / summary.headline misspelled headLine: /summary must NOT have additional properties (headLine)
- protoschema json-strict / summary.headline misspelled headLine: /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'headline'
- connect-openapi format=jsonschema / summary.headline misspelled headLine: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.headline misspelled headLine: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.headline misspelled headLine: /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / summary.headline misspelled headLine: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / summary.headline misspelled headLine: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / summary.origin.vendor misspelled vendr: /summary/origin must NOT have additional properties (vendr)
- protoschema json-strict / summary.origin.vendor misspelled vendr: /operation must have required property 'spanId'; /summary must have required property 'detail'; /summary must have required property 'interrupted'
- connect-openapi format=jsonschema / summary.origin.vendor misspelled vendr: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.origin.vendor misspelled vendr: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.origin.vendor misspelled vendr: /summary must be null; /summary must have required property 'missing'; /summary/origin/vendr must NOT be valid
- pubg (int64 only) / summary.origin.vendor misspelled vendr: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'detail'
- chrusty (archived, draft-04) / summary.origin.vendor misspelled vendr: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / summary.tier unknown enum name: /summary/tier must match pattern "^TIER_UNSPECIFIED$"; /summary/tier must be equal to one of the allowed values; /summary/tier must be integer
- protoschema json-strict / summary.tier unknown enum name: /operation must have required property 'spanId'; /summary must have required property 'detail'; /summary must have required property 'interrupted'
- connect-openapi format=jsonschema / summary.tier unknown enum name: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.tier unknown enum name: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.tier unknown enum name: /summary must be null; /summary must have required property 'missing'; /summary/tier must be equal to one of the allowed values
- pubg (int64 only) / summary.tier unknown enum name: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'detail'
- chrusty (archived, draft-04) / summary.tier unknown enum name: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / summary.durationMs not an integer: /summary/durationMs must be integer; /summary/durationMs must match pattern "^-?[0-9]+$"; /summary/durationMs must match a schema in anyOf
- protoschema json-strict / summary.durationMs not an integer: /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'interrupted'
- pubg (int64+presence) / summary.durationMs not an integer: /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / summary.durationMs not an integer: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / summary.durationMs not an integer: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json-strict / oneof cli: summary and resumed both set: /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'interrupted'
- connect-openapi format=jsonschema / oneof cli: summary and resumed both set: / must NOT be valid
- connect-openapi, protovalidate off / oneof cli: summary and resumed both set: / must NOT be valid
- pubg (int64+presence) / oneof cli: summary and resumed both set: / must have required property 'waiting'; / must match exactly one schema in oneOf; /summary must be null
- pubg (int64 only) / oneof cli: summary and resumed both set: / must have required property 'waiting'; / must match exactly one schema in oneOf; /operation must have required property 'spanId'
- chrusty (archived, draft-04) / oneof cli: summary and resumed both set: / must have required property 'waiting'; / must match exactly one schema in oneOf; /operation must have required property 'started'
- protoschema json (lenient) / operation.time not RFC 3339: /operation/time must match format "date-time"
- protoschema json-strict / operation.time not RFC 3339: /operation must have required property 'spanId'; /operation/time must match format "date-time"; /summary must have required property 'success'
- connect-openapi format=jsonschema / operation.time not RFC 3339: /operation/time must match format "date-time"
- connect-openapi, protovalidate off / operation.time not RFC 3339: /operation/time must match format "date-time"
- pubg (int64+presence) / operation.time not RFC 3339: /operation/time must match format "date-time"; /summary must be null; /summary must have required property 'propagation'
- pubg (int64 only) / operation.time not RFC 3339: /operation must have required property 'spanId'; /operation/time must match format "date-time"; /summary must be null
- chrusty (archived, draft-04) / operation.time not RFC 3339: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json (lenient) / operation.spanId not base64: /operation/spanId must match pattern "^[A-Za-z0-9+/]*={0,2}$"
- protoschema json-strict / operation.spanId not base64: /operation/spanId must match pattern "^[A-Za-z0-9+/]*={0,2}$"; /operation/started must have required property 'parentSpanId'
- connect-openapi format=jsonschema / operation.spanId not base64: /operation/spanId must NOT have more than 8 characters
- pubg (int64+presence) / operation.spanId not base64: /operation/spanId must match pattern "^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"
- pubg (int64 only) / operation.spanId not base64: /operation/spanId must match pattern "^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"; /operation/started must be null; /operation/started must have required property 'parentSpanId'
- chrusty (archived, draft-04) / operation.spanId not base64: / must have required property 'waiting'; / must have required property 'resumed'; / must have required property 'summary'
- protoschema json-strict / summary.durationMs as JSON number: /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'interrupted'
- connect-openapi format=jsonschema / summary.durationMs as JSON number: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.durationMs as JSON number: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.durationMs as JSON number: /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / summary.durationMs as JSON number: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / summary.durationMs as JSON number: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json-strict / summary.tier as enum number: /operation must have required property 'spanId'; /summary must have required property 'detail'; /summary must have required property 'interrupted'
- connect-openapi format=jsonschema / summary.tier as enum number: / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.tier as enum number: / must NOT have unevaluated properties
- pubg (int64+presence) / summary.tier as enum number: /summary must be null; /summary must have required property 'missing'; /summary/tier must be string
- pubg (int64 only) / summary.tier as enum number: /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'detail'
- chrusty (archived, draft-04) / summary.tier as enum number: /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'
- protoschema json-strict / summary.duration_ms (proto name): /operation must have required property 'spanId'; /summary must have required property 'success'; /summary must have required property 'durationMs'
- connect-openapi format=jsonschema / summary.duration_ms (proto name): / must NOT have unevaluated properties
- connect-openapi, protovalidate off / summary.duration_ms (proto name): / must NOT have unevaluated properties
- pubg (int64+presence) / summary.duration_ms (proto name): /summary must be null; /summary must have required property 'propagation'; /summary must have required property 'missing'
- pubg (int64 only) / summary.duration_ms (proto name): /operation must have required property 'spanId'; /summary must be null; /summary must have required property 'success'
- chrusty (archived, draft-04) / summary.duration_ms (proto name): /operation must have required property 'started'; /operation must have required property 'ended'; /operation must have required property 'output'

# probe.v1.Probe (encodings matrix)

| case | protojson | protoschema json (lenient) | protoschema json-strict | connect-openapi format=jsonschema | pubg (int64+presence) | pubg (int64 only) | chrusty (archived, draft-04) | pubg (int64+presence), required dropped | chrusty, required dropped |
|---|---|---|---|---|---|---|---|---|---|
| probe canonical protojson output | emit | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only big | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only ubig | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only small | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only ratio | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only blob | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only color | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only maybe | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | accept | accept | accept |
| probe output, only maybeBig | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only counts | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only inners | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only items | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only colors | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only at | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only took | emit | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only meta | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only anyValue | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only packed | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only wrappedBig | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only wrappedStr | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only nothing | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only mask | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | reject ✗ | reject ✗ |
| probe output, only inner | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe output, only snakeCaseName | emit | accept | reject ✗ | accept | reject ✗ | reject ✗ | reject ✗ | accept | reject ✗ |
| probe parse-only input (names, numbers, enum ints) | parse | reject | reject | reject | reject | reject | reject | reject | reject |
| probe oneof: text and inner both set | reject | reject | reject | reject | reject | reject | reject | reject | reject |
| probe took as ISO 8601 PT1S | reject | accept ✗ | reject | accept ✗ | reject | reject | reject | accept ✗ | reject |
| probe counts value not an integer | reject | reject | reject | accept ✗ | reject | reject | reject | reject | reject |
| probe misspelled field | reject | reject | reject | reject | reject | reject | reject | reject | reject |

## first errors per rejection (probe.v1.Probe (encodings matrix))

- protoschema json (lenient) / probe canonical protojson output: /took must match format "duration"
- protoschema json-strict / probe canonical protojson output: /big must be integer; /counts/a must be integer; /counts/b must be integer
- connect-openapi format=jsonschema / probe canonical protojson output: /took must match format "duration"
- pubg (int64+presence) / probe canonical protojson output: /counts must be string; /took must match format "duration"; /meta/k must NOT be valid
- pubg (int64 only) / probe canonical protojson output: /counts must be string; /took must match format "duration"; /meta/k must NOT be valid
- chrusty (archived, draft-04) / probe canonical protojson output: / must have required property 'maybe_big'; / must have required property 'text'; / must match exactly one schema in oneOf
- pubg (int64+presence), required dropped / probe canonical protojson output: /counts must be string; /took must match format "duration"; /meta/k must NOT be valid
- chrusty, required dropped / probe canonical protojson output: / must have required property 'maybe_big'; / must have required property 'text'; / must match exactly one schema in oneOf
- protoschema json-strict / probe output, only big: / must have required property 'ubig'; / must have required property 'small'; / must have required property 'ratio'
- pubg (int64+presence) / probe output, only big: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only big: / must have required property 'ubig'; / must have required property 'small'; / must have required property 'ratio'
- chrusty (archived, draft-04) / probe output, only big: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only big: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only ubig: / must have required property 'big'; / must have required property 'small'; / must have required property 'ratio'
- pubg (int64+presence) / probe output, only ubig: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only ubig: / must have required property 'big'; / must have required property 'small'; / must have required property 'ratio'
- chrusty (archived, draft-04) / probe output, only ubig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only ubig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only small: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'ratio'
- pubg (int64+presence) / probe output, only small: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only small: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'ratio'
- chrusty (archived, draft-04) / probe output, only small: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only small: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only ratio: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only ratio: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only ratio: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only ratio: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only ratio: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only blob: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only blob: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only blob: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only blob: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only blob: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only color: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only color: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only color: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only color: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only color: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only maybe: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only maybe: / must have required property 'maybeBig'; / must have required property 'at'; / must have required property 'took'
- pubg (int64 only) / probe output, only maybe: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- protoschema json-strict / probe output, only maybeBig: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only maybeBig: / must have required property 'maybe'; / must have required property 'at'; / must have required property 'took'
- pubg (int64 only) / probe output, only maybeBig: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only maybeBig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only maybeBig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only counts: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only counts: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only counts: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only counts: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only counts: /counts must be string
- chrusty, required dropped / probe output, only counts: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only inners: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only inners: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only inners: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only inners: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only inners: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only items: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only items: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only items: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only items: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only items: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only colors: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only colors: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only colors: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only colors: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only colors: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only at: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only at: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'took'
- pubg (int64 only) / probe output, only at: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only at: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only at: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json (lenient) / probe output, only took: /took must match format "duration"
- protoschema json-strict / probe output, only took: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- connect-openapi format=jsonschema / probe output, only took: /took must match format "duration"
- pubg (int64+presence) / probe output, only took: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only took: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only took: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only took: /took must match format "duration"
- chrusty, required dropped / probe output, only took: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only meta: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only meta: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only meta: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only meta: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only meta: /meta/k must NOT be valid
- chrusty, required dropped / probe output, only meta: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only anyValue: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only anyValue: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only anyValue: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only anyValue: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only anyValue: /anyValue must match exactly one schema in oneOf; /anyValue must be object
- chrusty, required dropped / probe output, only anyValue: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only packed: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only packed: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only packed: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only packed: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only packed: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only wrappedBig: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only wrappedBig: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only wrappedBig: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only wrappedBig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only wrappedBig: /wrappedBig must be object
- chrusty, required dropped / probe output, only wrappedBig: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only wrappedStr: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only wrappedStr: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only wrappedStr: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only wrappedStr: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only wrappedStr: /wrappedStr must be object
- chrusty, required dropped / probe output, only wrappedStr: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only nothing: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only nothing: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only nothing: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only nothing: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only nothing: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only mask: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only mask: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only mask: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only mask: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe output, only mask: /mask must be object
- chrusty, required dropped / probe output, only mask: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json-strict / probe output, only inner: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only inner: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only inner: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only inner: /inner must NOT have additional properties (name)
- chrusty, required dropped / probe output, only inner: /inner must NOT have additional properties (name)
- protoschema json-strict / probe output, only snakeCaseName: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe output, only snakeCaseName: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe output, only snakeCaseName: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe output, only snakeCaseName: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe output, only snakeCaseName: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json (lenient) / probe parse-only input (names, numbers, enum ints): /took must match format "duration"
- protoschema json-strict / probe parse-only input (names, numbers, enum ints): / must have required property 'snakeCaseName'; / must NOT have additional properties (maybe_big); / must NOT have additional properties (snake_case_name)
- connect-openapi format=jsonschema / probe parse-only input (names, numbers, enum ints): /big must be string; /counts/a must be string; /colors/1 must be string
- pubg (int64+presence) / probe parse-only input (names, numbers, enum ints): / must have required property 'maybeBig'; /maybe_big must NOT be valid; /snake_case_name must NOT be valid
- pubg (int64 only) / probe parse-only input (names, numbers, enum ints): / must have required property 'snakeCaseName'; /maybe_big must NOT be valid; /snake_case_name must NOT be valid
- chrusty (archived, draft-04) / probe parse-only input (names, numbers, enum ints): / must match exactly one schema in oneOf; / must NOT have additional properties (maybe_big); / must NOT have additional properties (snake_case_name)
- pubg (int64+presence), required dropped / probe parse-only input (names, numbers, enum ints): /maybe_big must NOT be valid; /snake_case_name must NOT be valid; /big must be string
- chrusty, required dropped / probe parse-only input (names, numbers, enum ints): / must match exactly one schema in oneOf; / must NOT have additional properties (maybe_big); / must NOT have additional properties (snake_case_name)
- protoschema json (lenient) / probe oneof: text and inner both set: /took must match format "duration"
- protoschema json-strict / probe oneof: text and inner both set: /big must be integer; /counts/a must be integer; /counts/b must be integer
- connect-openapi format=jsonschema / probe oneof: text and inner both set: / must NOT be valid; /took must match format "duration"
- pubg (int64+presence) / probe oneof: text and inner both set: / must match exactly one schema in oneOf; /counts must be string; /took must match format "duration"
- pubg (int64 only) / probe oneof: text and inner both set: / must match exactly one schema in oneOf; /counts must be string; /took must match format "duration"
- chrusty (archived, draft-04) / probe oneof: text and inner both set: / must have required property 'maybe_big'; / must match exactly one schema in oneOf; /inners/x must NOT have additional properties (name)
- pubg (int64+presence), required dropped / probe oneof: text and inner both set: / must match exactly one schema in oneOf; /counts must be string; /took must match format "duration"
- chrusty, required dropped / probe oneof: text and inner both set: / must have required property 'maybe_big'; / must match exactly one schema in oneOf; /inners/x must NOT have additional properties (name)
- protoschema json-strict / probe took as ISO 8601 PT1S: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe took as ISO 8601 PT1S: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe took as ISO 8601 PT1S: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe took as ISO 8601 PT1S: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- chrusty, required dropped / probe took as ISO 8601 PT1S: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json (lenient) / probe counts value not an integer: /counts/a must be integer; /counts/a must match pattern "^-?[0-9]+$"; /counts/a must match a schema in anyOf
- protoschema json-strict / probe counts value not an integer: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- pubg (int64+presence) / probe counts value not an integer: / must have required property 'maybe'; / must have required property 'maybeBig'; / must have required property 'at'
- pubg (int64 only) / probe counts value not an integer: / must have required property 'big'; / must have required property 'ubig'; / must have required property 'small'
- chrusty (archived, draft-04) / probe counts value not an integer: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- pubg (int64+presence), required dropped / probe counts value not an integer: /counts must be string
- chrusty, required dropped / probe counts value not an integer: / must have required property 'maybe'; / must have required property 'maybe_big'; / must have required property 'text'
- protoschema json (lenient) / probe misspelled field: / must NOT have additional properties (snakeCaseNam); /took must match format "duration"
- protoschema json-strict / probe misspelled field: / must NOT have additional properties (snakeCaseNam); /big must be integer; /counts/a must be integer
- connect-openapi format=jsonschema / probe misspelled field: /took must match format "duration"; / must NOT have unevaluated properties
- pubg (int64+presence) / probe misspelled field: /snakeCaseNam must NOT be valid; /counts must be string; /took must match format "duration"
- pubg (int64 only) / probe misspelled field: /snakeCaseNam must NOT be valid; /counts must be string; /took must match format "duration"
- chrusty (archived, draft-04) / probe misspelled field: / must have required property 'maybe_big'; / must have required property 'text'; / must match exactly one schema in oneOf
- pubg (int64+presence), required dropped / probe misspelled field: /snakeCaseNam must NOT be valid; /counts must be string; /took must match format "duration"
- chrusty, required dropped / probe misspelled field: / must have required property 'maybe_big'; / must have required property 'text'; / must match exactly one schema in oneOf
