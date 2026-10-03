#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# Run the current checkout rather than downloading an upstream executable.
go run ../../.. -index=Index.xlsx -json_out=table_gen.json
