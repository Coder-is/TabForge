#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
protoc -I examples/protocol/proto --include_imports --include_source_info \
  --descriptor_set_out=examples/protocol/schema.pb examples/protocol/proto/chat.proto
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated
