#!/usr/bin/env bash
# Thin wrapper so the workflow's existing path keeps working. The real script
# is scripts/e2e-shared/guard-accounts.sh, shared with scripts/e2e-node — see
# that file for what it checks and why.
set -euo pipefail
exec "$(dirname "${BASH_SOURCE[0]}")/../e2e-shared/guard-accounts.sh"
