#!/usr/bin/env bash
# jq 探测、按文件分类、敏感键、解码视图、fromjson。把 TEXT_TARGETS 改成
# 非 JSON 文件 + 解码视图（带路径映射）+ JSON 原文的规范化副本。
# 对象键：非普通标识符，或命中 sk-/JWT/probe-<24hex>/≥16 的十六进制或 base64url，打印 <key len=N>。
# auth/authorization 用路径上最后一个对象键（数组下标不算）；方式词后允许多个空格。
JQ_PROG='
def is_ph: test("^(<redacted>|<redacted:[a-z0-9_]+>|[*]+)$");
def sens:
  ascii_downcase as $n
  | ($n | test($field_core)) or ($n | test("key$")) or $n == "auth" or $n == "sig";
def unwrap:
  if type == "string" and test("^\\[.*\\]$") then .[1:length-1] else . end;
def auth_secret:
  unwrap as $u
  | if ($u | test("^(basic|bearer|token|api-?key|digest) +"; "i")) then
      ($u | sub("^(basic|bearer|token|api-?key|digest) +"; ""; "i") | unwrap)
    else $u end;
def bad($v): ($v | length) >= 6 and ($v | is_ph | not);
def hide($k):
  (($k | test("^[A-Za-z_][A-Za-z0-9_-]{0,31}$")) | not)
  or ($k | test("^sk-[A-Za-z0-9_-]{16,}$"))
  or ($k | test("^eyJ[A-Za-z0-9_-]{8,}$"))
  or ($k | test("^probe-[0-9a-f]{24}$"))
  or ($k | test("^[0-9A-Fa-f]{16,}$"))
  or ($k | test("^[A-Za-z0-9_-]{16,}$"));
def fmt($seg):
  if $seg[0] == "i" then ($seg[1] | tostring)
  elif $seg[0] == "j" then "(json)"
  else
    ($seg[1]) as $k
    | if hide($k) then "<key len=\($k | length)>" else $k end
  end;
def joinpath($path): [$path[] | fmt(.)] | join(".");
def sens_path($path): any($path[]; .[0] == "k" and (.[1] | sens));
def last_obj_key($path):
  reduce $path[] as $s (""; if $s[0] == "k" then $s[1] else . end);
def emit($path):
  if type == "object" then
    to_entries[] | .key as $k | .value | emit($path + [["k", $k]])
  elif type == "array" then
    to_entries[] | .key as $i | .value | emit($path + [["i", $i]])
  elif type == "string" then
    (
      if sens_path($path) then
        (last_obj_key($path) | ascii_downcase) as $lk
        | (if ($lk == "auth" or $lk == "authorization") then auth_secret else . end) as $judge
        | if bad($judge) then "S\t" + joinpath($path) else empty end
      else empty end
    ),
    (
      joinpath($path) as $p
      | split("\n")[] | "D\t" + $p + "\t" + .
    ),
    (
      (try fromjson catch null) as $j
      | if $j != null and (($j | type) == "object" or ($j | type) == "array") then
          $j | emit($path + [["j"]])
        else empty end
    )
  else empty end;
emit([])
'

if ! jq -n -e 1 >/dev/null 2>&1; then
  bad=1
  echo "FAIL jq 不可用"
  return 0
fi

: >"$W/json"
: >"$W/nonjson"
while IFS= read -r -d '' f; do
  if [ ! -r "$f" ]; then
    continue
  fi
  jq -e . "$f" >/dev/null 2>"$W/jq.err"
  rc=$?
  if { [ "$rc" -eq 0 ] || [ "$rc" -eq 1 ]; } && [ ! -s "$W/jq.err" ]; then
    printf '%s\0' "$f" >>"$W/json"
  else
    printf '%s\0' "$f" >>"$W/nonjson"
  fi
done <"$W/files"

: >"$W/text.targets"
: >"$W/dv.list"
: >"$W/field.jq.locs"
if [ -s "$W/nonjson" ]; then
  cat "$W/nonjson" >>"$W/text.targets"
fi
mkdir -p "$W/dv" "$W/jf"
dv_i=0
jf_i=0
if [ -s "$W/json" ]; then
  while IFS= read -r -d '' f; do
    jq -r --arg field_core "$FIELD_CORE" "$JQ_PROG" "$f" >"$W/jq.out" 2>"$W/jq.err"
    rc=$?
    if [ "$rc" -ne 0 ] || [ -s "$W/jq.err" ]; then
      bad=1
      echo "FAIL ${FIELD_RULE}（jq 出错 rc=${rc}）"
      printf '  %s\n' "$f"
    else
      : >"$W/dv/$dv_i"
      : >"$W/dv/$dv_i.map"
      printf '%s\n' "$f" >"$W/dv/$dv_i.src"
      while IFS= read -r line || [ -n "$line" ]; do
        [ -n "$line" ] || continue
        kind=${line%%$'\t'*}
        rest=${line#*$'\t'}
        if [ "$kind" = S ]; then
          printf '%s:%s\n' "$f" "$rest" >>"$W/field.jq.locs"
        elif [ "$kind" = D ]; then
          path=${rest%%$'\t'*}
          content=${rest#*$'\t'}
          printf '%s\n' "$content" >>"$W/dv/$dv_i"
          printf '%s\n' "$path" >>"$W/dv/$dv_i.map"
        fi
      done <"$W/jq.out"
      printf '%s\0' "$W/dv/$dv_i" >>"$W/dv.list"
      printf '%s\0' "$W/dv/$dv_i" >>"$W/text.targets"
      dv_i=$((dv_i + 1))
    fi
    normalize_to "$f" "$W/jf/$jf_i"
    printf '%s\n' "$f" >"$W/jf/$jf_i.src"
    printf '%s\0' "$W/jf/$jf_i" >>"$W/text.targets"
    jf_i=$((jf_i + 1))
  done <"$W/json"
fi
report "$FIELD_RULE" "$(sort -u "$W/field.jq.locs")"
# 解码视图补扫（redact-10）直接调用，不存在就失败；它的 14 次计入本模块。
if ! declare -F basic_scan_dv >/dev/null; then
  bad=1
  echo "FAIL 解码视图补扫函数未定义：basic_scan_dv"
fi
basic_scan_dv
# 每次恰好 15 次：敏感字段 1、basic_scan_dv 14。
MOD_RULES_20=15; MOD_DONE_20=1
