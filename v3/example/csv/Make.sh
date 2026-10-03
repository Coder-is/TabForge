#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

go run ../../.. \
-index=Index.csv \
-go_out=../golang/table_gen.go \
-json_out=../json/table_gen.json \
-jsontype_out=../jsontype/type_gen.json \
-lua_out=../lua/table_gen.lua \
-csharp_out=../csharp/TabtoyExample/table_gen.cs \
-binary_out=../binary/table_gen.bin \
-java_out=../java/src/main/java/main/Table.java \
-package=main

cp ../json/table_gen.json ../java/cfg/table_gen.json
