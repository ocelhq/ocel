#!/usr/bin/env bash
(
  set -e

  backup=.env.local.gateway-backup
  test ! -e "$backup"
  mv .env.local "$backup"

  restore_env() {
    test ! -f "$backup" || mv "$backup" .env.local
  }
  trap restore_env EXIT

  pnpm deepsec setup \
    --project-id ocelhq \
    --agent codex \
    --model gpt-5.6-sol \
    --thinking-level high \
    --model-auth local
)
