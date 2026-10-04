#!/bin/bash
TABFORGE_LAUNCHER_DIR="${BASH_SOURCE[0]%/*}"
if [ "$TABFORGE_LAUNCHER_DIR" = "${BASH_SOURCE[0]}" ]; then TABFORGE_LAUNCHER_DIR=.; fi
cd -- "$TABFORGE_LAUNCHER_DIR" || exit 1
./tabforge -project=../../tabforge.json
result=$?
if [ "$result" -ne 0 ]; then
  echo "导出失败，以上信息包含出错位置。"
fi
read -r -p "按回车关闭窗口..."
exit "$result"
