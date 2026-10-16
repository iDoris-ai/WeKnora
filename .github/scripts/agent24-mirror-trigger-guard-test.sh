#!/usr/bin/env bash
# 守卫脚本的正反例。用法：bash .github/scripts/agent24-mirror-trigger-guard-test.sh
# 在临时 git 仓库里逐例提交一个工作流文件，断言守卫的退出码。
set -uo pipefail
G="$(cd "$(dirname "$0")" && pwd)/agent24-mirror-trigger-guard.rb"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
cd "$T" && git init -q
fail=0
run() { # 期望退出码 分支 用例名 文件名 内容
  rm -rf .github; mkdir -p .github/workflows; printf '%s\n' "$5" > ".github/workflows/$4"
  git add -A; git -c user.name=t -c user.email=t@t commit -qm x --allow-empty
  ruby "$G" "$(git rev-parse HEAD)" "$2" >/dev/null 2>&1; rc=$?
  if [ "$rc" = "$1" ]; then echo "PASS $3"; else echo "FAIL $3 (exp=$1 got=$rc)"; fail=1; fi
  git rm -rq --cached . >/dev/null 2>&1
}
M=upstream-main; R=upstream-release
run 1 $M "push 无过滤"              w.yml $'on:\n  push:\njobs: {}'
run 1 $M "on: push"                 w.yml $'on: push\njobs: {}'
run 1 $M "on 列表"                  w.yml $'on: [push, pull_request]\njobs: {}'
run 1 $M "branches **"              w.yml $'on:\n  push:\n    branches: ["**"]\njobs: {}'
run 1 $M "branches upstream-*"      w.yml $'on:\n  push:\n    branches: [upstream-*]\njobs: {}'
run 1 $M "branches ?"               w.yml $'on:\n  push:\n    branches: ["upstream-mains?"]\njobs: {}'
run 1 $M "branches +"               w.yml $'on:\n  push:\n    branches: ["upstream-mai+n"]\njobs: {}'
run 1 $M "branches []"              w.yml $'on:\n  push:\n    branches: ["upstream-[mr]*"]\njobs: {}'
run 1 $M "未闭合 ["                 w.yml $'on:\n  push:\n    branches: ["upstream-[m"]\njobs: {}'
run 1 $M "ignore 未排除（?）"       w.yml $'on:\n  push:\n    branches-ignore: ["upstream-mai?"]\njobs: {}'
run 1 $M "ignore [main]"            w.yml $'on:\n  push:\n    branches-ignore: [main]\njobs: {}'
run 1 $M "只有 paths"               w.yml $'on:\n  push:\n    paths: ["**/*.go"]\njobs: {}'
run 1 $M "create"                   w.yml $'on:\n  create:\njobs: {}'
run 1 $M "非 ASCII 文件名"          发布.yml $'on: push\njobs: {}'
run 1 $M "含引号文件名"             'a"b.yml' $'on: push\njobs: {}'
run 1 $M ".yaml 扩展名"             w.yaml $'on: push\njobs: {}'
run 1 $M "锚点别名"                 w.yml $'x: &p\n  branches: ["**"]\non:\n  push: *p\njobs: {}'
run 1 $M "YAML 无法解析"            w.yml $'on:\n  push: [\njobs: {}'
run 1 $M "顶层不是映射"             w.yml $'- a\n- b'
run 0 $M "branches [main]"          w.yml $'on:\n  push:\n    branches: [main]\njobs: {}'
run 0 $M "main + tags"              w.yml $'on:\n  push:\n    branches: [main]\n    tags: ["v*"]\njobs: {}'
run 0 $M "只有 tags"                w.yml $'on:\n  push:\n    tags: ["v*"]\njobs: {}'
run 0 $M "ignore upstream-*"        w.yml $'on:\n  push:\n    branches-ignore: ["upstream-*"]\njobs: {}'
run 0 $M "镜像 main 的 PR 工作流不查" w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
run 1 $R "PR 未声明 permissions"    w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
run 1 $R "PR contents: write"       w.yml $'on: pull_request\npermissions:\n  contents: write\njobs:\n  a:\n    runs-on: x'
run 1 $R "PR job write-all"         w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    permissions: write-all'
run 1 $R "pull_request_review 未声明" w.yml $'on: pull_request_review\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR 只读"                  w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR read-all"              w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR permissions {}"        w.yml $'on: pull_request\npermissions: {}\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR 只有 id-token write"   w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    permissions:\n      id-token: write'
run 0 $R "pull_request_target 不查" w.yml $'on: pull_request_target\njobs:\n  a:\n    runs-on: x'
exit $fail
