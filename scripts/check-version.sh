#!/usr/bin/env bash
set -euo pipefail

# VERSION 是发布版本的唯一来源；拒绝空白、前导零和多行内容。
repo_root=$(cd "$(dirname "$0")/.." && pwd)
if ! node -e '
  const fs = require("fs");
  const value = fs.readFileSync(process.argv[1]);
  const source = value.toString("utf8");
  const canonical = /^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\n$/;
  process.exit(Buffer.from(source, "utf8").equals(value) && canonical.test(source) ? 0 : 1);
' "$repo_root/VERSION"; then
  echo 'VERSION must contain exactly one canonical X.Y.Z version' >&2
  exit 1
fi
