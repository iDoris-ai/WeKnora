# UI 只读路由清单

`docs/agent24/ui-routes.json` 是 Knowledge 嵌入页可以调用的 UI 只读入口清单。服务端注册的是同一份列表（`internal/agent24uisession.UIReadRoutes`），顺序一致。

格式：顶层 `routes` 数组。每项有 `method`、`path`、`op`。`path` 是 gin 的 FullPath，参数段写成 `:name`。`*slug` 只允许出现在路径末尾。本文件不带 `source`；宿主钉副本时自己加 `source.commit`。

会话 JWT 的 `ops` 是这份清单里实际放行的 op，按 `UIOps` 的顺序去重。改清单会同时改变放行的路由和 JWT 的 ops。PR 里要提醒 Knowledge 侧一起改嵌入页。
