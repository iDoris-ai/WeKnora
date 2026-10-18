# KB-CT-01 探测框架（PR②）

这套脚本对本机按当前登记的基线（现为 v0.8.3）起的标准栈逐项实测 `docs/agent24/CAPABILITY-MATRIX.md` 中已提供探测函数的能力项，脱敏后输出证据。证据目录按受测 SHA 命名（`probe-evidence/<受测 HEAD 的 8 位 SHA>`）；每份证据的 `environment.tested_dir_note` 由脚本按登记的 release 生成，脚本同时核对 fork 命名空间之外与该 release 逐字节相同，不同就以 3 停下。旧目录 `4b363d15` 是 v0.8.2 的实测，保留不动。

框架自带一个示例项（第 9 项）。其余探测项放在 `probes.sh` 里，文件存在时由 `run.sh` 自动加载。

| 文件 | 作用 |
|---|---|
| `run.sh` | 入口：解析项号，统一按升序排好；source `lib.sh`、`probe-09-health.sh`，`probes.sh` 存在时也一并 source |
| `lib.sh` | 框架：前置核对、setup、请求记录、emit、伪 Ollama、清理与 trap、凭据替换、证据落盘 |
| `probe-09-health.sh` | 示例探测项（矩阵第 9 项 /health） |
| `check-redaction.sh` | 脱敏自检驱动：参数、临时目录，按 `REQUIRED_MODULES` 检查并 source `redact-NN-*.sh` |
| `redact-00-lib.sh` | 公共函数与敏感字段名；定义 `TEXT_TARGETS`，不跑规则 |
| `redact-10-basic.sh` | sk-、JWT、Bearer、Cookie、邮箱、`$HOME`/`$USER`、`.env` 等 |
| `redact-20-json.sh` | jq 分类、敏感键、解码视图、JSON 原文的规范化副本 |
| `redact-30-text.sh` | 字段、数组、KV、query、凭据头 |
| `check-redaction-selftest.sh` | 脱敏自测框架（按 `REQUIRED_SELFTESTS` 逐个在子 shell 里 source `selftest-NN-*.sh`） |
| `selftest-10-basic.sh` | 基础规则自测 |
| `selftest-20-json.sh` | jq 路径自测 |
| `selftest-30-text.sh` | 文本规则自测（不依赖 jq 模块） |
| `REDACTION.md` | 脱敏规则说明 |

## 1. 起栈：按受测栈约束

约束以 `docs/agent24/UPSTREAM-BASELINE.json` 的 `current_baseline.test_stack_constraints` 为准，镜像 digest 以同一节的 `image_digests` 为准。

默认的 5 个服务只用到 `docker-compose.yml`、`.env.example` 和 `config/`（app 挂载了 `config/config.yaml`）。从 WeKnora 仓库（上游或本 fork 均可）取出 v0.8.3 的这几个文件：

```bash
mkdir -p ~/weknora-probe-stack
git -C <WeKnora 仓库> archive v0.8.3 docker-compose.yml .env.example config \
  | tar -x -C ~/weknora-probe-stack
cd ~/weknora-probe-stack && cp .env.example .env
```

然后改 `.env`：
- `WEKNORA_VERSION=v0.8.3`。Docker Hub 上的 `latest` 会随上游前进，不能依赖。
- `APP_PORT=127.0.0.1:8080`；frontend 只需绑 127.0.0.1，端口任意，如 `FRONTEND_PORT=127.0.0.1:18080`（探测只访问 app）。
- `STORAGE_TYPE=local`、`GIN_MODE=release`、`LANGFUSE_ENABLED=false`；`JWT_SECRET`、`SYSTEM_AES_KEY` 这类密钥填随机值。

起栈按约束的顺序来（每条命令都带 `env -u` 前缀，不依赖 shell 状态）：

```bash
env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE docker compose config --images   # 须正好是登记的 5 个引用，不符就停
env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE docker compose pull              # 之后核对 5 个默认服务的 RepoDigests 都含登记的 digest，不一致就停、不起栈
env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE docker compose up -d --no-build  # 只起默认的 5 个服务（app、docreader、frontend、postgres、redis），不启用 profile
```

起栈后确认各容器的 image ID 等于比对过的镜像。tag 被改指、image ID 不一致时的处置以约束原文为准。不要用 `scripts/start_all.sh`，它默认带 `--pull always`，`--no-pull` 则会用当前 checkout 重建镜像。

预计耗时：
- 首次拉取 5 个镜像约 10.4GB（docreader 5.5GB、paradedb 2.4GB、app 2.3GB、ui 与 redis 合计约 0.2GB）。按 100 Mbps 估算约 15 分钟。
- 起栈和首次迁移约 1–2 分钟。
- 运行时间：如果 1–16 项都已提供，跑一遍约 30 秒（本机实测 29 秒，Mac mini + OrbStack）；只跑示例项也要先建全部 setup 数据，约 10 秒（本机实测 none 模式 7 秒、fake-ollama 9 秒）。

其他注意事项：
- 误跑过 `latest` 的数据卷要丢弃重建。Lite 与标准栈的结果不能互相替代。
- **只支持从 Docker Hub 拉取的官方镜像。** 约束里也允许"从 3e22822 本地构建"，但本地构建的镜像没有 RepoDigests，无法与登记的 digest 比对，前置核对会失败。要支持这种做法，得先另行登记本地构建的镜像 ID 和构建方式；本脚本没有做。

## 2. 前置核对（不符就以退出码 3 停下）

前置核对失败时，证据目录保持原样。脚本按顺序检查：

1. 依赖齐全：curl、jq、docker、openssl、python3；`$STACK_DIR/.env` 可读（脱敏自检要用它比对密钥值）。
2. `BASE` 只能是 `http://127.0.0.1:端口`，而且端口必须等于 app 经 `docker port` 发布的宿主端口。
3. 运行中的服务正好是默认的 5 个，所有发布端口都只绑 127.0.0.1（frontend 端口任意，本机是 `127.0.0.1:18080`）。
4. app 容器的 `STORAGE_TYPE=local`。数据库用户和库名也从 app 容器的 `DB_USER`、`DB_NAME` 读出；psql 连不上时报"无法连接数据库"。
5. 5 个默认服务（app、frontend、docreader、postgres、redis）的镜像 digest 都与登记值一致：先用 `docker inspect -f '{{.Image}}'` 取容器实际使用的镜像 ID，再查这个 ID 的 `RepoDigests`。登记里读不到 digest 也算失败。
6. `schema_migrations` 等于登记值（118）。
7. 宿主 `127.0.0.1:11434`：
   - `fake-ollama` 模式下必须空闲，否则停下；
   - `none` 模式下有应答时也停下。原因是 `.env` 的 `OLLAMA_BASE_URL` 指向宿主 11434，产品可能会调用宿主上的 Ollama 去拉取不存在的模型。确认无妨时可设 `PROBE_ALLOW_HOST_OLLAMA=1`，此时改为打 WARN，并记进 `environment.warnings`。
8. `/system/info` 的 `commit_id`（至少 7 位前缀）、`db_version` 与登记一致。这个接口要鉴权，所以只能在注册并登录第一个测试用户之后核对；不符同样以 3 退出，不再写入其他数据。

第 1–7 项都在写入任何数据之前完成。核对结果写进每个证据文件的 `environment`，包括全部 `docker port` 输出。

## 3. 运行方式

```bash
scripts/agent24/probe/run.sh 9                           # 只跑第 9 项，证据写到临时目录
PROBE_EMBED=fake-ollama scripts/agent24/probe/run.sh 9   # 同上，用伪 Ollama
```

- **参数与执行顺序**：每个参数都必须是十进制项号（前导 0 按十进制处理），空串、`9abc` 这类参数以退出码 2 拒绝；校验通过后按项号升序、去重执行。
- **网络**：脚本开头清掉 `http_proxy`、`https_proxy`、`all_proxy`（含大写形式），所有 curl 都带 `-q`（不读用户的 `~/.curlrc`，其中可能开了 `location` 之类会跟随重定向的选项）和 `--noproxy '*'`，只访问 127.0.0.1。
- **凭据不上命令行**：本机其他用户能看到进程参数，所以凭据都经文件或标准输入交给子进程：请求头（主体凭据和 `xh` 追加的凭据头）写进 0700 工作目录里的文件，用 `curl -H @文件`；JSON 请求体用 `--data-binary @文件`；自签 JWT 的 HMAC 密钥经标准输入交给 python；收尾替换凭据、`.env` 比对、日志里按凭据查找，都经标准输入或 `grep -f`。
- **环境变量**：
  - `BASE`、`STACK_DIR`；
  - `OUT`：最终证据目录；
  - `PROBE_EMBED`：只接受 `none` 和 `fake-ollama`，其他值以退出码 2 退出；
  - `PROBE_ALLOW_HOST_OLLAMA`：见第 2 节第 7 条。
- **写到哪里**：
  - 正式证据目录 `docs/agent24/probe-evidence/<受测 SHA>/`：只有同时满足"1–16 项都已提供并全部运行、`PROBE_EMBED=fake-ollama`、没有显式指定 `OUT`"时才写。
  - 没有显式指定 `OUT` 的其他情况，一律写到 `${TMPDIR:-/tmp}/weknora-probe-partial.*`，并打印路径。
  - 显式指定 `OUT` 时，**这个目录会被整体替换**，替换后只剩本次运行的证据。正式证据目录同样整体替换。下面两种情况以退出码 2 拒绝，目录不动：
    - `OUT`（显式指定的目录或正式证据目录）已存在且非空，里面有任何不是证据文件（`NN-*.json`、`13-log-body.txt`）的文件或子目录；
    - 显式指定 `OUT` 时只跑部分项，而 `OUT` 已存在且非空。
  - 写临时目录的运行中途失败时，这个临时目录如果还是空的，退出时会删掉。
- **原子落盘**：
  - 证据先写进 mktemp 工作目录；
  - 跑完、替换掉本次凭据、`check-redaction.sh` 通过之后，复制到 `OUT` 同级的临时目录，逐个比对一致；
  - 最后用 rename 整体替换 `OUT`，替换后 `OUT` 里只有本次运行的文件，目录权限按当前 umask（不是 mktemp 的 0700）。
  - 任何一步失败或被中断，都不会改动原来的 `OUT`：中断时 trap 会删掉复制了一半的 `.new.*`；如果旧 `OUT` 已挪成 `.old.*` 而新的还没就位，trap 会把它恢复回去。
  - 被 SIGKILL 打断时 trap 来不及执行，`OUT` 同级可能残留 `.<名>.new.*` 或 `.<名>.old.*`。处理方式：`.new.*` 直接删掉；`.old.*` 在 `OUT` 还在时删掉，`OUT` 不在且只有一个时恢复成 `OUT`，有多个时以退出码 2 停下，交给人处理。
    - 正式证据目录旁边的残留：下次运行开始时就处理，不论这次写到哪里；
    - 显式指定的 `OUT` 旁边的残留：下次写同一个 `OUT` 时处理；
    - 部分运行写到 `${TMPDIR:-/tmp}` 的临时目录被打断时留下的 `.weknora-probe-partial.*.new.*`、`.old.*` 不会自动清理（下次的临时目录名不同），需要自行删除。
  - 脱敏自检未通过时不删证据：整批挪到新建的 `${TMPDIR:-/tmp}/weknora-probe-failed.*`（权限 0700）并打印路径，`OUT` 不动。里面可能还有凭据，排查后请删除。
- **退出码**：
  - 0：成功；
  - 1：运行中出错；
  - 2：参数或 `OUT` 不合要求；
  - 3：前置核对失败；
  - 4：证据已写出，但有项"与矩阵不一致"；
  - 信号中断：INT 130、TERM 143、HUP 129。

  运行结束时会打印各项 verdict 汇总，有待回写矩阵的差异（`pending_writeback`）时逐条列在该项下面。
- **数据库**：只读。会话设 `default_transaction_read_only=on`，只用于读取校验。
- **测试数据**：全部经 API 建立，会留在栈里。

## 4. PROBE_EMBED 与伪 Ollama

v0.8.2 中，只开关键词的 KB 也需要 embedding：`IndexingStrategy.NeedsEmbedding()` 等于 vector||keyword。本机没有真实的 embedding 模型，不处理的话，文档解析会在 embedding 阶段失败，分块被删除。

- `none`（默认值）：不造 embedding。宿主 11434 有服务时默认停下，见第 2 节第 7 条。
- `fake-ollama`：
  - 在宿主 `127.0.0.1:11434` 起一个伪 Ollama，按文本哈希返回 8 维固定向量。query 带 `PROBE-SLOW` 时延迟 8 秒，供"取消"探测使用。
  - 启动后用 `/api/tags` 探测就绪，进程退出或起不来就失败退出。
  - 正常退出、出错或被 INT/TERM/HUP 中断时，trap 都会停掉它。如果脚本本身被 SIGKILL（trap 来不及执行），伪 Ollama 会残留，用下面的命令清理：

    ```bash
    lsof -ti tcp:11434 -sTCP:LISTEN | xargs kill
    ```
  - app 通过 `.env` 里已有的 `OLLAMA_BASE_URL=http://host.docker.internal:11434` 访问它，不改 `.env`、不加容器。
  - 依赖它的结论都带说明：`environment.embedding_note`、`conclusion.embedding_note`，以及检查名后缀"（依赖伪 embedding）"。**向量为伪造的固定值，只证明分块和读取路径，不代表检索质量。**
- **平台限制**：OrbStack 与 Docker Desktop 会把 `host.docker.internal` 转到宿主 loopback，已在 OrbStack 上验证。原生 Linux Docker 的 `host-gateway` 指向 docker0 网桥地址，访问不到只绑 127.0.0.1 的伪 Ollama。替代做法有两种：
  - 在 macOS 的 OrbStack 或 Docker Desktop 上运行；
  - 自行把伪 Ollama 改绑到网桥地址，并用防火墙只放行容器网段。本脚本不提供这种做法，也没有验证过。

## 5. 框架接口：setup 建的数据

| 类别 | 内容（变量） |
|---|---|
| 运行标识 | `RID`（UTC 时间加随机数）、`PW`（本次口令）、`ENVJ`（前置核对结果）、`EN`/`FD`（伪 embedding 说明） |
| 用户 | A：租户 `TA` 的 Owner；V：以 viewer 加入 `TA`；C：以 contributor 加入 `TA`；X：另一租户 `T_X`。变量是 `U_*`、`J_*`；`TN_V`、`TN_C` 用来附带 `X-Tenant-ID` |
| KB | KB1；KB2；KBX（属于 X）；KBV（开向量，模型指向不存在的 Ollama 模型）；KBF（仅伪模式：开向量，用伪模型） |
| 文档 | `D_ALPHA`（alpha.txt，在 KB1）、`D_HTML`（probe.html，在 KB1）、`D_BETA`（beta.txt，在 KB2）、`D_GAMMA`（gamma.txt，在 KBX）；`FP_ALPHA` 是 alpha 的 file_path，`PS`/`PERR` 是解析状态与错误 |
| API key | `K_*`、`KID_*`：empty（retrieve，白名单为空）、kb1（retrieve，白名单只有 KB1）、noretr（只有 run_evaluations）、chat（chat + retrieve）、full（full_access）、mvs（manage_vector_stores）、rev（retrieve，供撤销） |
| 凭据清单 | `SECRETS`：收尾时从证据里替换为 `<redacted>`。HMAC 哨兵都带随机后缀，不能由 RID 推出 |

探测项可用的函数：
- `call` 发请求，`J` 发 JSON 请求。`call` 每次先清空上一条响应；记录里的 `file_path` 只留协议头。遇 HTTP 000（传输失败）默认中止整次运行；`ALLOW_000=1` 只放行紧接着的这一次 `call`（含 `J` 再调的 `call`），返回后恢复。预期连不上的探测不要走 `call`，否则整次运行会停。
- `bj`、`hd`、`has` 读响应体、响应头，判断是否含某串（`has` 按固定字符串匹配）；
- `f`、`fv` 把字段附到上一条请求记录。`f` 的 jq 表达式输出多个值时合成数组，没有输出或出错时记 null；附加失败直接退出，不会丢掉已有记录；
- `xh 头名 值` 给紧接着的下一次 `call` 追加一个凭据头（如 `X-External-User-Token`），同样经文件交给 curl，值同时记入 `SECRETS`。凭据头不要写成 `call` 的 `-H` 参数，那样会出现在 curl 的进程参数里；
- `sgrep 串 [grep 选项…] [文件…]` 按固定字符串查找，查找串经 `-f` 读入、不上命令行（查凭据时用它）；
- `ck` 记录对照矩阵的检查；`ck_absent` 同样记录，但输入为空时记 ok=false、actual 为“输入为空，无法判定”；`obs` 记录观察；
- `emit` 输出证据，可附伪 embedding 说明和待回写差异；
- `take`、`want` 在 setup 里取值并校验；
- `sql` 只读查询，`applog` 读 app 日志。

## 6. 示例项：第 9 项 /health

`probe-09-health.sh` 演示探测项的写法：`call` 发请求，`ck` 对照矩阵，`obs` 记事实，`emit` 写出 `09-health.json`。

只跑这一项时，`run.sh` 仍会建好全部 setup 数据，再把证据写到打印出的临时目录。临时工作目录和伪 Ollama 在退出时由 trap 清理；这次的证据留在打印出的目录里，栈里的测试数据也会保留。

## 7. 脱敏与清理

`check-redaction.sh [--skip-env] [目录]` 扫描证据，参数顺序不限。它按 `REQUIRED_MODULES` 加载同目录的 `redact-NN-*.sh`（公共函数、与解析无关的规则、jq 路径、文本规则）。每个编号必须恰好有一个文件，缺一个就失败。不认识的名字不加载。模块 source 失败、没有跑到末尾、在模块里 `exit`，或本模块 `report` 的实际次数与它声明的 `MOD_RULES_NN` 不等，都失败。`REQUIRED_MODULES` 必须包含 `00` 和 `10`，目录里有而列表里没有的 `redact-NN-*.sh` 也算失败。叠放的中间片改 `REQUIRED_MODULES` 这一行，只留下本片已有的模块号。命中即失败，只打印规则名和位置，不打印值。真正把 token 挡在证据外的是 finalize 的凭据剔除和这次自检，不是日志摘录的长度（第 13 项只摘录哨兵前后各不超过 100 个字符）。规则、占位符和已知局限见 [REDACTION.md](REDACTION.md)。

规则自测不依赖证据目录，运行时在临时目录里逐例生成夹具：

```bash
scripts/agent24/probe/check-redaction-selftest.sh
```

清理：
- 临时工作目录、伪 Ollama 进程由 trap 在退出时清理，出错或被中断时也一样。
- 只跑部分项时，证据留在打印出的临时目录里，需要时自行删除。
- 栈里的测试数据会保留。

## 8. 第 1–16 项（probes.sh）

`probes.sh` 提供第 1–8、10–16 项，第 9 项见第 6 节。各项用到的 setup 数据：

| 项 | 依赖的数据 |
|---|---|
| 1 | KB1、KB2；key empty/kb1/noretr/full；jwt A |
| 2 | KB1、T_X、U_A；key chat/full；jwt A；只读 audit_logs |
| 3 | KB1、KBV、KBX、KBF（伪模式，取消探测）；key empty；app 日志 |
| 4 | D_ALPHA、D_BETA、PERR；key empty/kb1；jwt A（改写 beta 分块，看 revisions） |
| 5 | KB1、KB2、D_ALPHA、D_BETA、FP_ALPHA；key empty/kb1/noretr；jwt V/C/X；只读 resource_access_grants |
| 6 | D_ALPHA、D_HTML；key empty；jwt V |
| 7 | key empty/mvs；jwt V |
| 8 | key noretr/empty |
| 10 | KB1、KB2、KBX、D_GAMMA、T_X；key empty/full；jwt A/V；只读 users |
| 11 | key rev/chat、KID_rev、U_C、U_V、D_ALPHA；jwt A/C/V。会撤 key、给 C 降权、移除 V |
| 12 | jwt A、PW、RID（另注册用户 R） |
| 13 | KB1、D_ALPHA；key empty；jwt A；PW；app 日志；app 容器的 Langfuse 变量（只记是否非空） |
| 14 | app 容器环境变量（整项未测：需改环境并重启） |
| 15 | app 容器 |
| 16 | 无 |

第 11 项会撤 key、降权、移除成员，所以执行顺序统一按项号升序：它必须排在用到这些数据的项之后。

## 9. 正式证据

正式证据必须**全量**运行，用伪 Ollama 模式，并且不指定 `OUT`；只有这样才会写入正式证据目录（见第 3 节）：

```bash
PROBE_EMBED=fake-ollama scripts/agent24/probe/run.sh
```

其他组合（部分项、none 模式）都写临时目录，不会覆盖正式证据。

读结果：
- 文件名与矩阵一致；第 13 项是 `13-log-body.txt`，其余是 JSON。
- JSON 字段：
  - `environment`：前置核对的结果；
  - `requests`：每个请求的主体、方法、路径、状态码，`fields` 是与结论相关的字段；
  - `checks`：期望值取自矩阵；
  - `observations`：只记录事实，不参与判定；
  - `conclusion`：判定结果，见下。
- `conclusion.verdict` 只有四种取值：
  - `与矩阵一致`
  - `已测部分与矩阵一致；其余未测`
  - `与矩阵不一致（见 mismatches）`
  - `未测`
- `conclusion` 的其他字段：
  - `untested`：写明未测的部分和原因；
  - `embedding_note`：结论依赖伪 embedding 时才有；
  - `pending_writeback`：列出已发现、待回写矩阵的差异。

复现要求：需要第二个人在干净环境中复现（新机器，或删掉旧卷重建）。按第 1 节起栈后，运行上面的全量命令，逐项比对 `conclusion.verdict` 和 `checks[].ok`。run id、各种 ID 和时间不同属于正常。复现结果回帖到 iDoris-ai/WeKnora#5。

## 10. 证据里没写全的未测范围

这一节是对已提交证据的补充，证据文件本身不改。

- 第 3 项（hybrid-search）另有未测范围：引擎解析阶段的 12s 内部上限、跨多个 store group 时每组的 30s 内部上限，以及 `/knowledge-search`。
- 第 13 项（日志是否记录正文）另有未测范围：访问日志的 10KB 截断、local-browser 与 SSE 不记录、`LOG_LEVEL=warn` / `LOG_LEVEL=error` 的分级。
