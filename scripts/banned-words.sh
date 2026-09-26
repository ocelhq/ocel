#!/usr/bin/env bash
set -euo pipefail

banned=(standing substrate substrates held carried settle settled settles settling settlement unsettled owe owed owes owing)
allowed='allSettled|PromiseSettledResult|settled(Within|Pool)\b|json:"(owed|settled)\b|\block\b.*\bheld\b|\bheld\b.*\block\b|\bheld open\b'

words=$(IFS='|' && echo "${banned[*]}")
capitalised=$(IFS='|' && echo "${banned[*]^}")
pattern="(?<![A-Za-z])(?i:$words)(?![a-z])|(?<=[a-z0-9])(?:$capitalised)(?![a-z])"

hits=$({
  git grep -P -I -n "$pattern" -- . ':(exclude)scripts/banned-words.sh' \
    ':(exclude,glob)**/gen/**' ':(exclude,glob)*/**/proto/**' ':(exclude,glob)**/worker-configuration.d.ts' \
    ':(exclude,glob)**/*.lock' ':(exclude,glob)**/*-lock.yaml' ':(exclude,glob)**/*.sum' || true
  git ls-files | grep -P "$pattern" || true
} | grep -Pv "^[^:]+:[0-9]+:.*(?:$allowed)" || true)

if [ -n "$hits" ]; then
  printf '%s\n' "$hits"
  echo "banned words: name the actual state instead (.greptile/rules.md, Naming rule 9)" >&2
  exit 1
fi
