#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# Rebuild after changing the .proto files; imported messages are included.
protoc -I proto --include_imports --descriptor_set_out=schema.pb proto/config.proto

go run ../../.. -mode=v3 -index=Index.csv \
  -proto_desc=schema.pb -proto_map=mapping.json \
  -pbbin_out=out/tables.pbb -pbjson_out=out/tables.json -pbbin_dir=out/by-table

# Optional: generate Go messages with protoc-gen-go, then read out/tables.pbb.
# protoc -I proto --go_out=. --go_opt=module=github.com/Coder-is/TabForge/v3/example/existingproto \
#   proto/common.proto proto/config.proto
