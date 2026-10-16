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
run 1 $M "叠加量词 *+"              w.yml $'on:\n  push:\n    branches: ["upstream-*+n"]\njobs: {}'
run 1 $M "叠加量词 **+"             w.yml $'on:\n  push:\n    branches: ["**+main"]\njobs: {}'
run 1 $M "方括号 [^x] 排除"         w.yml $'on:\n  push:\n    branches-ignore: ["upstream-[^x]ain"]\njobs: {}'
run 1 $M "方括号 [m&&r]"            w.yml $'on:\n  push:\n    branches: ["upstream-[m&&r]ain"]\njobs: {}'
run 1 $M "方括号 [!x]"              w.yml $'on:\n  push:\n    branches: ["upstream-[!x]ain"]\njobs: {}'
run 1 $M "ignore 里 ! 重新包含"     w.yml $'on:\n  push:\n    branches-ignore: ["upstream-*", "!upstream-main"]\njobs: {}'
run 1 $M "YAML 合并键"              w.yml $'s: &s\n  branches: [main]\non:\n  push:\n    branches: [upstream-main]\n    <<: *s\njobs: {}'
run 0 $M "方括号 [a-z]* 不匹配"     w.yml $'on:\n  push:\n    branches: ["release-[0-9]*"]\njobs: {}'
run 0 $M "branches [main]"          w.yml $'on:\n  push:\n    branches: [main]\njobs: {}'
run 0 $M "main + tags"              w.yml $'on:\n  push:\n    branches: [main]\n    tags: ["v*"]\njobs: {}'
run 0 $M "只有 tags"                w.yml $'on:\n  push:\n    tags: ["v*"]\njobs: {}'
run 0 $M "ignore upstream-*"        w.yml $'on:\n  push:\n    branches-ignore: ["upstream-*"]\njobs: {}'
run 0 $M "镜像 main 的 PR 工作流不查" w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
# 没有 .github/workflows 目录：放行
rm -rf .github; echo x > README; git add -A; git -c user.name=t -c user.email=t@t commit -qm x
if ruby "$G" "$(git rev-parse HEAD)" "$M" >/dev/null 2>&1; then echo "PASS 无工作流目录"; else echo "FAIL 无工作流目录"; fail=1; fi
git rm -rq --cached . >/dev/null 2>&1; rm -f README
# 目标提交不存在：失败
if ruby "$G" 0000000000000000000000000000000000000000 "$M" >/dev/null 2>&1; then echo "FAIL 提交不存在"; fail=1; else echo "PASS 提交不存在"; fi
run 1 $R "PR 未声明 permissions"    w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
run 1 $R "PR contents: write"       w.yml $'on: pull_request\npermissions:\n  contents: write\njobs:\n  a:\n    runs-on: x'
run 1 $R "PR job write-all"         w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    permissions: write-all'
run 1 $R "pull_request_review 未声明" w.yml $'on: pull_request_review\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR 只读"                  w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR read-all"              w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR permissions {}"        w.yml $'on: pull_request\npermissions: {}\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR 只有 id-token write"   w.yml $'on: pull_request\npermissions:\n  contents: read\njobs:\n  a:\n    permissions:\n      id-token: write'
run 1 $R "PR 引用组织 secret"       w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    steps:\n      - env:\n          T: ${{ secrets.CLA_TOKEN }}'
run 1 $R "PR toJSON(secrets)"       w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    steps:\n      - run: echo \'${{ toJSON(secrets) }}\''
run 1 $R "PR secrets[...]"          w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    steps:\n      - env:\n          T: ${{ secrets[\'CLA_TOKEN\'] }}'
run 1 $R "PR secrets: inherit"      w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    uses: ./.github/workflows/x.yml\n    secrets: inherit'
run 1 $R "merge_group 未声明"       w.yml $'on: merge_group\njobs:\n  a:\n    runs-on: x'
run 0 $R "PR 引用白名单 secret"     w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    steps:\n      - id: secrets\n        env:\n          H: ${{ secrets.WEKNORA_E2E_HOST }}\n      - if: steps.secrets.outputs.skip == \'false\'\n        env:\n          G: ${{ secrets.GITHUB_TOKEN }}'
run 0 $R "PR shell 文本里的 secrets.x" w.yml $'on: pull_request\npermissions: read-all\njobs:\n  a:\n    steps:\n      - run: helm template --set secrets.dbPassword=test'
run 0 $R "pull_request_target 不查" w.yml $'on: pull_request_target\njobs:\n  a:\n    runs-on: x'
exit $fail
