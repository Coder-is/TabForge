#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

mkdir -p ../jsondir ../luadir ../binary ../protobuf

go run ../../.. \
-index=Index.xlsx \
-go_out=../golang/table_gen.go \
-json_out=../json/table_gen.json \
-json_dir=../jsondir \
-lua_out=../lua/table_gen.lua \
-lua_dir=../luadir \
-binary_dir=../binary \
-csharp_out=../csharp/TabtoyExample/table_gen.cs \
-binary_out=../binary/table_gen.bin \
-java_out=../java/src/main/java/main/Table.java \
-proto_out=../protobuf/table.proto \
-pbbin_out=../protobuf/all.pbb \
-pbbin_dir=../protobuf \
-package=main

cp ../json/table_gen.json ../java/cfg/table_gen.json
