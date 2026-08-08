#!/usr/bin/env bash
# Thin wrapper, same as scripts/e2e-next/guard-accounts.sh. The real script is
# scripts/e2e-shared/guard-accounts.sh — see that file for what it checks.
set -euo pipefail
exec "$(dirname "${BASH_SOURCE[0]}")/../e2e-shared/guard-accounts.sh"
