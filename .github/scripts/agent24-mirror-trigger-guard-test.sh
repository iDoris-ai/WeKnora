#!/usr/bin/env bash
# 守卫脚本的正反例。用法：bash .github/scripts/agent24-mirror-trigger-guard-test.sh
# 在临时 git 仓库里逐例提交一个工作流文件，断言守卫的退出码。
set -uo pipefail
# 继承的仓库定位变量（例如从 git hook 里运行）会让临时仓库上的操作落到真实仓库
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
G="$(cd "$(dirname "$0")" && pwd)/agent24-mirror-trigger-guard.rb"
T=$(mktemp -d) || exit 1; trap 'rm -rf "$T"' EXIT
cd "$T" || exit 1; git init -q || exit 1
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

# 镜像推进的失败路径：非快进时停止、不强推。从工作流里取出推进镜像那一步的脚本，在临时裸仓库上原样运行；
# gh 用桩代替，上游地址经 url.insteadOf 指向本地仓库，其他 https://github.com/ 地址和 https 推送改写到不存在的目录；
# GIT_ALLOW_PROTOCOL=file 让 git 只用本地协议，git@github.com:、ssh:// 之类的地址直接被拒。不访问网络，不碰真实镜像分支。
F="$T/ff"; mkdir -p "$F/bin"
ruby -ryaml -e 'puts YAML.load_file(ARGV[0])["jobs"]["sync"]["steps"].find { |s| s["name"] == "Fast-forward mirrors and propose release integration" }["run"]' \
  "$(dirname "$G")/../workflows/agent24-upstream-sync.yml" > "$F/step.sh" 2>&1 || { echo "FAIL 取出推进镜像的步骤"; fail=1; }
# 先拼接续行；整个脚本里都不许出现强制（+ 开头）或删除（: 开头）的 refspec
awk '{ if (sub(/\\$/, "")) printf "%s", $0; else print }' "$F/step.sh" > "$F/joined.sh"
pushes=$(grep 'git push' "$F/joined.sh")
first_push=$(grep -n 'git push' "$F/joined.sh" | head -1 | cut -d: -f1)
last_check=$(grep -n 'git merge-base --is-ancestor origin/upstream-' "$F/joined.sh" | tail -1 | cut -d: -f1)
if [ "$(head -1 "$F/joined.sh")" = "set -euo pipefail" ] && [ -n "$pushes" ] \
  && ! grep -qE -- '--force|--mirror|--delete|--prune|(^|[[:space:]])-[a-zA-Z]*f' <<<"$pushes" \
  && ! grep -qE "(^|[[:space:]\"'(])[+:][^[:space:]]*refs/" "$F/joined.sh" \
  && [ "$(grep -c 'git merge-base --is-ancestor origin/upstream-' "$F/joined.sh")" = 2 ] && [ "$last_check" -lt "$first_push" ]; then
  echo "PASS 推送不带强推，两条祖先检查都在推送之前"
else echo "FAIL 推送不带强推，两条祖先检查都在推送之前"; fail=1; fi
cat > "$F/bin/gh" <<'SH'; chmod +x "$F/bin/gh"
#!/usr/bin/env bash
echo "$*" >> "$GH_LOG"
case "$1 $2|$*" in
  "api repos/Tencent/WeKnora/releases/latest|"*) echo v1.0.0 ;;
  "pr list|"*"--head ${PR_HEAD:-none} "*) echo 7 ;;
esac
exit 0
SH
# 不读开发者的全局与系统 git 配置（pre-push hook、push.gpgSign 等会让本地误报）
nocfg() { GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 "$@"; }
g() { nocfg git -c user.name=t -c user.email=t@t -c init.defaultBranch=main -c advice.detachedHead=false "$@"; }
ff_run() { # 期望退出 用例名 上游main 上游正式版 [noverify|pr-main|pr-release]
  # 历史：R─base─A─B，X 从 base 分出，Y 从 R 分出。upstream-main 现在 A，upstream-release 现在 base（起点不同，
  # 祖先检查拿错镜像就会被 B X 用例抓到）；a24/develop 是 B 与 X 的合并，正例都在"已包含"处退出、不开 issue。
  local mode=${5:-}  # bash 4+ 在 set -u 下展开未设置的 $5 会报错
  rm -rf "$F/up" "$F/origin.git" "$F/work" "$F/gh.log"
  ( g init -q "$F/up" && cd "$F/up" && for c in R base A B; do g commit -q --allow-empty -m $c || exit 1; done \
    && g checkout -q main~2 && g commit -q --allow-empty -m X && g update-ref refs/heads/X HEAD \
    && g checkout -q main~3 && g commit -q --allow-empty -m Y && g update-ref refs/heads/Y HEAD \
    && g checkout -q --detach main && g merge -q --no-ff --no-edit X && g update-ref refs/heads/M HEAD && g init -q --bare "$F/origin.git" \
    && g push -q "$F/origin.git" main~1:refs/heads/upstream-main main~2:refs/heads/upstream-release M:refs/heads/a24/develop ) || { echo "FAIL $2（准备仓库）"; fail=1; return; }
  local A B X Y SR; A=$(g -C "$F/up" rev-parse main~1) && B=$(g -C "$F/up" rev-parse main) && X=$(g -C "$F/up" rev-parse X) \
    && Y=$(g -C "$F/up" rev-parse Y) && SR=$(g -C "$F/up" rev-parse main~2) || { echo "FAIL $2（准备仓库）"; fail=1; return; }
  local m=${!3} r=${!4}; g -C "$F/up" update-ref refs/heads/main "$m" && g -C "$F/up" tag -f v1.0.0 "$r" >/dev/null || { echo "FAIL $2（准备仓库）"; fail=1; return; }
  g clone -q -b a24/develop "$F/origin.git" "$F/work" && mkdir -p "$F/work/.github/scripts" && cp "$G" "$F/work/.github/scripts/" \
    || { echo "FAIL $2（准备仓库）"; fail=1; return; }
  # 步骤写的 /tmp/upstream-sync-issue.md 换成指向 $F 下的变量（被测步骤退化、走到建 issue 时也不写测试目录之外）；
  # 用变量而不是把 $F 拼进替换串，临时目录里有空格或 sed 的特殊字符也不出错。其他 /tmp 路径先在原步骤里查。
  if sed 's#/tmp/upstream-sync-issue\.md##g' "$F/step.sh" | grep -q '/tmp/'; then
    echo "FAIL $2（步骤里有未改写的 /tmp 路径）"; fail=1; return
  fi
  if [ "$mode" = noverify ]; then grep -v 'git merge-base --is-ancestor origin/upstream-' "$F/step.sh"; else cat "$F/step.sh"; fi \
    | sed 's#/tmp/upstream-sync-issue\.md#"$AGENT24_TEST_ISSUE_FILE"#g' > "$F/run.sh"
  (cd "$F/work" && env PATH="$F/bin:$PATH" GH_LOG="$F/gh.log" PR_HEAD="upstream-${mode#pr-}" GITHUB_REPOSITORY=example/sandbox GITHUB_STEP_SUMMARY="$F/summary" \
    GIT_CONFIG_COUNT=3 GIT_CONFIG_KEY_0="url.$F/up.insteadOf" GIT_CONFIG_VALUE_0=https://github.com/Tencent/WeKnora.git \
    GIT_CONFIG_KEY_1="url.$F/blocked/.insteadOf" GIT_CONFIG_VALUE_1=https://github.com/ \
    GIT_CONFIG_KEY_2="url.$F/blocked/.pushInsteadOf" GIT_CONFIG_VALUE_2=https:// GIT_TERMINAL_PROMPT=0 GIT_ALLOW_PROTOCOL=file \
    AGENT24_TEST_ISSUE_FILE="$F/upstream-sync-issue.md" \
    LC_ALL=C GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 bash --noprofile --norc -eo pipefail "$F/run.sh") > "$F/out" 2>&1; local rc=$?
  local om or; om=$(g --git-dir="$F/origin.git" rev-parse upstream-main); or=$(g --git-dir="$F/origin.git" rev-parse upstream-release)
  local em=$m er=$r; [ "$mode" = pr-main ] && em=$A; [ "$mode" = pr-release ] && er=$SR
  # 负例要钉住失败位置：已从本地上游取到 main（insteadOf 生效）、查过正式版，且停在 pr list 之前
  if [ "$1" = 0 ]; then [ "$rc" = 0 ] && [ "$om" = "$em" ] && [ "$or" = "$er" ]; else
    [ "$rc" != 0 ] && [ "$om" = "$A" ] && [ "$or" = "$SR" ] && ! grep -q '^issue ' "$F/gh.log" 2>/dev/null \
      && grep -q '^api repos/Tencent/WeKnora/releases/latest' "$F/gh.log" && [ "$(g -C "$F/work" rev-parse -q --verify refs/remotes/upstream/main)" = "$m" ] \
      && { [ "$mode" = noverify ] || ! grep -q '^pr list' "$F/gh.log" 2>/dev/null; } \
      && { [ "$mode" != noverify ] || grep -q 'non-fast-forward' "$F/out"; }; fi \
    && echo "PASS $2" || { echo "FAIL $2 (rc=$rc)"; sed 's/^/    /' "$F/out"; fail=1; }
}
ff_run 0 "两条镜像都快进：推进（对照）" B B
ff_run 0 "正式版只是 upstream-release 的后代：推进（两条祖先检查各查各的镜像）" B X
ff_run 0 "upstream-main 有同仓 PR：只推进 upstream-release" B B pr-main
ff_run 0 "upstream-release 有同仓 PR：只推进 upstream-main" B B pr-release
ff_run 1 "upstream-main 非快进：停止，两条镜像都不动" X B
ff_run 1 "upstream-release 非快进：停止，两条镜像都不动" B Y
ff_run 1 "去掉祖先检查，推送本身也拒绝 upstream-main 非快进（不强推）" X B noverify
ff_run 1 "去掉祖先检查，推送本身也拒绝 upstream-release 非快进（不强推）" B Y noverify
exit $fail
