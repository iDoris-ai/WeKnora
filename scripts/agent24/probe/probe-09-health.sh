# shellcheck shell=bash
# 示例探测项（矩阵第 9 项 /health），演示框架的调用方式：call 发请求、ck 对照矩阵、obs 记事实、emit 出证据。
p9() {
  call none GET /health
  ck "/health 无鉴权 200" 200 "$ST"
  ck "写死返回 ok" ok "$(bj .status)"
  call none GET /ready
  obs "无凭据 /ready（未注册路由先过全局 Auth）" "$ST"
  call key:empty GET /ready
  ck "没有 /ready（带凭据得 404）" 404 "$ST"
  emit 9 09-health.json "/health" "依赖故障时是否仍返回 ok 未测（不能停依赖）"
}
