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
  out=$(ruby "$G" "$(git rev-parse HEAD)" "$2" 2>&1); rc=$?
  # 期望拦下的用例必须是守卫自己检出（输出 ::error::），不能是崩溃；期望放行的必须输出 ok
  if [ "$1" = 1 ]; then grep -q '^::error::' <<<"$out" && ! grep -q 'Error)' <<<"$out" || rc=crash; else grep -q '^mirror trigger guard: ok$' <<<"$out" || rc=crash; fi
  if [ "$rc" = "$1" ]; then echo "PASS $3"; else echo "FAIL $3 (exp=$1 got=$rc)"; fail=1; fi
  git rm -rq --cached . >/dev/null 2>&1
}
M=upstream-main
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
run 1 $M "flow 映射里的合并键"      w.yml $'on:\n  push: {branches: ["**"], <<: {branches: [main]}}\njobs: {}'
run 1 $M "加引号的合并键"           w.yml $'s: &s\n  branches: [main]\non:\n  push:\n    branches: [upstream-main]\n    "<<": *s\njobs: {}'
run 1 $M "YAML 合并键"              w.yml $'s: &s\n  branches: [main]\non:\n  push:\n    branches: [upstream-main]\n    <<: *s\njobs: {}'
run 1 $M "开头 **/*（零层目录）"    w.yml $'on:\n  push:\n    branches: ["**/*"]\njobs: {}'
run 1 $M "开头 **/**"              w.yml $'on:\n  push:\n    branches: ["**/**"]\njobs: {}'
run 1 $M "开头 **/upstream-main"   w.yml $'on:\n  push:\n    branches: ["**/upstream-main"]\njobs: {}'
run 1 $M "开头 **/upstream-*"      w.yml $'on:\n  push:\n    branches: ["**/upstream-*"]\njobs: {}'
run 1 upstream-release "开头 **/upstream-release" w.yml $'on:\n  push:\n    branches: ["**/upstream-release"]\njobs: {}'
run 1 $M "结尾 /**（零层存疑）"     w.yml $'on:\n  push:\n    branches: ["upstream-main/**"]\njobs: {}'
run 1 $M "ignore **/ 不算一定排除" w.yml $'on:\n  push:\n    branches-ignore: ["**/upstream-main"]\njobs: {}'
run 1 $M "反斜杠转义"              w.yml $'on:\n  push:\n    branches: ["upstream\\\\-main"]\njobs: {}'
run 1 $M "多模式里任一匹配"        w.yml $'on:\n  push:\n    branches: [main, "upstream-*"]\njobs: {}'
run 1 $M "多文档（第二个是 push）" w.yml $'on: pull_request\njobs: {}\n---\non: push\njobs: {}'
run 1 $M "重复键 on（后者不是 push）" w.yml $'on: push\non: pull_request\njobs: {}'
run 1 $M "\"on\" 与裸 on 并存"      w.yml $'on: push\n"on": pull_request\njobs: {}'
run 1 $M "裸 on 与 true 并存"       w.yml $'on: push\ntrue: pull_request\njobs: {}'
run 1 $M "嵌套重复键 branches"     w.yml $'on:\n  push:\n    branches: [upstream-main]\n    branches: [main]\njobs: {}'
run 1 $M "复杂键"                  w.yml $'? [on]\n: push\non: pull_request\njobs: {}'
run 1 $M "ignore 结尾 /** 不算一定排除" w.yml $'on:\n  push:\n    branches-ignore: ["upstream-main/**"]\njobs: {}'
run 1 $M "***/（与 ** 相邻的 / 可省略）" w.yml $'on:\n  push:\n    branches: ["***/upstream-main"]\njobs: {}'
run 1 $M "结尾 /**/**"             w.yml $'on:\n  push:\n    branches: ["upstream-main/**/**"]\njobs: {}'
run 1 $M "大小写不同"              w.yml $'on:\n  push:\n    branches: ["UPSTREAM-MAIN"]\njobs: {}'
run 0 $M "ignore 大小写不同不算排除" w.yml $'on:\n  push:\n    branches: [main]\n    branches-ignore: ["UPSTREAM-MAIN"]\njobs: {}'
run 1 $M "ignore 大小写不同不算排除（只有 ignore）" w.yml $'on:\n  push:\n    branches-ignore: ["UPSTREAM-MAIN"]\njobs: {}'
run 1 $M "带标签的键"              w.yml $'on: pull_request\n!!str x: 1\njobs: {}'
run 1 $M "转义写法的合并键"        w.yml $'s: &s\n  branches: [main]\non:\n  push:\n    branches: ["**"]\n    "\\x3c\\x3c": *s\njobs: {}'
run 1 $M "转义写法的合并键（\\u）" w.yml $'s: &s\n  branches: [main]\non:\n  push:\n    branches: ["**"]\n    "\\u003c<": *s\njobs: {}'
run 0 $M "run 脚本里的 <<: 不误拦" w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: |\n          echo "a<<: b"\n          cat <<: x'
run 1 $M "全名 refs/heads/upstream-main" w.yml $'on:\n  push:\n    branches: ["refs/heads/upstream-main"]\njobs: {}'
run 1 $M "全名 refs/heads/**"      w.yml $'on:\n  push:\n    branches: ["refs/heads/**"]\njobs: {}'
run 1 $M "全名 refs/heads/*"       w.yml $'on:\n  push:\n    branches: ["refs/heads/*"]\njobs: {}'
run 1 $M "全名 */*/upstream-main"  w.yml $'on:\n  push:\n    branches: ["*/*/upstream-main"]\njobs: {}'
run 1 upstream-release "全名 refs/heads/upstream-release" w.yml $'on:\n  push:\n    branches: ["refs/heads/upstream-release"]\njobs: {}'
run 1 $M "带标签的值（解码后为 pull_request）" w.yml $'on: !!binary cHVsbF9yZXF1ZXN0\njobs: {}'
run 1 $M "集合上的标签 !!seq"      w.yml $'on:\n  push:\n    branches: !!seq [main]\njobs: {}'
run 1 $M "集合上的标签 !!map"      w.yml $'on:\n  push: !!map {branches: [main]}\njobs: {}'
run 1 $M "带标签的序列项（解码后为 main）" w.yml $'on:\n  push:\n    branches: [!!binary bWFpbg==]\njobs: {}'
run 0 $M "方括号 [a-z]* 不匹配"     w.yml $'on:\n  push:\n    branches: ["release-[0-9]*"]\njobs: {}'
run 1 $M "branches: [] 空列表"       w.yml $'on:\n  push:\n    branches: []\njobs: {}'
run 1 $M "大写扩展名 .YML"          W.YML $'on: push\njobs: {}'
run 1 $M "内容含非法 UTF-8"         w.yml $'on: push\njobs: {}\n# \xff'
run 1 upstream-release "推 upstream-release 也查" w.yml $'on:\n  push:\n    branches: ["upstream-r*"]\njobs: {}'
run 0 upstream-release "PR 工作流不在推送检查范围" w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
run 0 $M "branches [main, **/release]" w.yml $'on:\n  push:\n    branches: [main, "**/release"]\njobs: {}'
run 0 $M "全名 refs/heads/main 不误拦" w.yml $'on:\n  push:\n    branches: ["refs/heads/main"]\njobs: {}'
run 0 $M "branches [main]"          w.yml $'on:\n  push:\n    branches: [main]\njobs: {}'
run 0 $M "main + tags"              w.yml $'on:\n  push:\n    branches: [main]\n    tags: ["v*"]\njobs: {}'
run 0 $M "只有 tags"                w.yml $'on:\n  push:\n    tags: ["v*"]\njobs: {}'
run 0 $M "ignore upstream-*"        w.yml $'on:\n  push:\n    branches-ignore: ["upstream-*"]\njobs: {}'
run 0 $M "PR 工作流不查" w.yml $'on: pull_request\njobs:\n  a:\n    runs-on: x'
# 没有 .github/workflows 目录：放行
rm -rf .github; echo x > README; git add -A; git -c user.name=t -c user.email=t@t commit -qm x
if ruby "$G" "$(git rev-parse HEAD)" "$M" 2>&1 | grep -q '^mirror trigger guard: ok$'; then echo "PASS 无工作流目录"; else echo "FAIL 无工作流目录"; fail=1; fi
git rm -rq --cached . >/dev/null 2>&1; rm -f README
# 工作流文件是符号链接：拦下
rm -rf .github; mkdir -p .github/workflows; printf 'on:\n  pull_request:\njobs: {}\n' > .github/workflows/real.txt; ln -s real.txt .github/workflows/w.yml
git add -A; git -c user.name=t -c user.email=t@t commit -qm x
out=$(ruby "$G" "$(git rev-parse HEAD)" "$M" 2>&1)
if grep -q '^::error::.*符号链接' <<<"$out"; then echo "PASS 符号链接"; else echo "FAIL 符号链接"; fail=1; fi
git rm -rq --cached . >/dev/null 2>&1
# .github/workflows 或 .github 本身是符号链接：拦下
for d in .github/workflows .github; do
  rm -rf .github elsewhere; mkdir -p elsewhere/workflows; printf 'on: push\njobs: {}\n' > elsewhere/workflows/w.yml
  mkdir -p "$(dirname "$d")"; [ "$d" = .github ] && ln -s elsewhere .github || ln -s ../elsewhere/workflows .github/workflows
  git add -A; git -c user.name=t -c user.email=t@t commit -qm x
  out=$(ruby "$G" "$(git rev-parse HEAD)" "$M" 2>&1)
  if grep -q '^::error::.*不是普通目录' <<<"$out"; then echo "PASS $d 是符号链接"; else echo "FAIL $d 是符号链接"; fail=1; fi
  git rm -rq --cached . >/dev/null 2>&1; rm -rf .github elsewhere
done
# 目标提交不存在：失败
if ruby "$G" 0000000000000000000000000000000000000000 "$M" >/dev/null 2>&1; then echo "FAIL 提交不存在"; fail=1; else echo "PASS 提交不存在"; fi
exit $fail
