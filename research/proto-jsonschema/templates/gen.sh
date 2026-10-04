#!/usr/bin/env bash
# Generates JSON Schema for proto/cli/stream (the run-event stream) and for the
# research probe message with each candidate plugin. Run from anywhere.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
cd "$repo"
for plugin in protoschema connect-openapi connect-openapi-noprotovalidate pubg pubg-default chrusty; do
  template="research/proto-jsonschema/templates/buf.gen.$plugin.yaml"
  echo "== $plugin (stream)"
  buf generate --template "$template"
  echo "== $plugin (probe)"
  sed -e 's#directory: proto$#directory: research/proto-jsonschema/probe/proto#' \
    -e '/paths:/d' -e '/- proto\/cli\/stream/d' \
    -e "s#research/proto-jsonschema/gen/$plugin#research/proto-jsonschema/gen/probe/$plugin#" \
    -e 's#entrypoint_message=RunEvent#entrypoint_message=Probe#' \
    -e 's#ocel.cli.jsonschema.json#probe.jsonschema.json#' \
    "$template" >"research/proto-jsonschema/.work/buf.gen.probe.$plugin.yaml"
  buf generate --template "research/proto-jsonschema/.work/buf.gen.probe.$plugin.yaml"
done
