# 脱敏自检规则

`check-redaction.sh [--skip-env] [目录]` 扫描一个证据目录。参数顺序不限。不带目录时逐个扫描 `docs/agent24/probe-evidence/` 下的全部子目录（每个在子进程里单独跑，任何一个失败即以 1 失败；隐藏目录是 `replace_out` 的残留，跳过并打印）。驱动只做参数、`LC_ALL=C`、临时目录，然后按 `REQUIRED_MODULES`（本片是 `00 10 20 30`）检查同目录的 `redact-NN-*.sh`。每个编号必须恰好有一个文件，再按这个顺序 `source`，并打印实际加载的模块。缺一个就打印 `FAIL 缺少脱敏模块 NN` 并以 1 退出。不认识的名字不加载。`source` 失败会打印 `FAIL 模块加载出错` 并以 1 退出。每个模块在最后声明完成哨兵 `MOD_DONE_NN=1` 和 `MOD_RULES_NN`，后者是本模块在任何输入下都恰好调用 `report` 的次数：出错分支不 `report` 时也补记一次；解码视图上的补扫只在命中时打印，但每条每次都计一次（没有 JSON、解码视图为空时也一样）。驱动在 source 每个模块前把计数清零，并 `unset` 环境里同名的 `MOD_DONE_NN`、`MOD_RULES_NN`，之后逐模块核对，哨兵缺失、声明不是 0–9999 的整数、实际次数与声明不等（打印模块名和两边的数字），都失败。模块里执行 `exit`（任何退出码）时，驱动打印 `FAIL 驱动未正常结束（模块提前退出）` 并以 1 退出。`REQUIRED_MODULES` 必须包含 `00` 和 `10`；目录里有 `redact-NN-*.sh` 而 NN 不在列表里，也失败。叠放的中间片改 `REQUIRED_MODULES` 这一行，只留下本片已有的模块号，所以中间片单独也能跑。

grep 都带 `-a`。规则是 ASCII。命中只打印规则名，以及 `文件:行号` 或 `文件:JSON路径`，不打印值。

- 读不到 `$STACK_DIR/.env`（默认 `$HOME/weknora-probe-stack/.env`）时失败。只有显式传 `--skip-env` 才跳过。
- 不带目录参数时：`probe-evidence/` 已入库，必须存在、是目录、可读、可进入；否则（包括不存在，以及上级目录 `docs/`、`docs/agent24/` 不可进入导致无法判断）无法枚举，打印 `FAIL 证据根目录不可读、不可进入或不存在` 并以 1 退出。只有能枚举且其下没有（非隐藏的）子目录时，打印说明后以 0 退出，并写明「没有检查任何文件」。
- 目录里有不可读的普通文件即失败。某条规则的 grep 退出码 ≥2 也失败，不能被后面的过滤盖住。

## 占位符

只有**整个值**是占位符才放过：字面 `<redacted>`、`<redacted:标签>`（标签是 `[a-z0-9_]+`），或纯 `*`。按「字段或头 + 占位符」整段锚定，所以 `"api_key":"真值=<redacted>"`、`<redacted 真值>` 照样报。

## 文件怎么分流

`redact-20-json.sh` 先跑 `jq -n -e 1`。失败就打印 `FAIL jq 不可用` 并结束本模块；后面的文本模块仍会扫。此时 `TEXT_TARGETS` 保持「目录下全部文件」。

jq 可用时，逐个可读文件跑 `jq -e .`。退出码是 0 或 1，且 stderr 为空，走 jq。其他任何退出码，或 stderr 非空（含空文件），改走正则。jq 遍历本身出错则 `FAIL` 敏感字段规则并带上 `jq 出错`。

走 jq 的文件会多出两份供文本规则使用的副本：

- 解码视图：每个字符串叶子按换行拆开，旁边有 JSON 路径映射。
- JSON 原文的规范化副本（见下）。普通遍历会折叠重复键，只留下最后一个值。整文件能解析时再用 `jq -c --stream` 看流式事件，让第一个值也按敏感键规则判断，位置同样写成 `文件:JSON路径`，同一条路径不重复打印。这一遍只补敏感路径上的命中，不为第一个值生成解码视图，所以非敏感键下的字符串（如 `"msg":"password=…"`）只按最后一个值判断。键名带 `.`、空格、`:`，或第一个值是对象时，规范化副本上的字段正则认不出，要靠这一遍。第一个值是字符串标量、键名没有被反斜杠转义、且键名只含 `[A-Za-z0-9_-]` 时，副本上的字段正则也能报出。

`TEXT_TARGETS` 因此变成：非 JSON 文件 + 解码视图 + JSON 原文的规范化副本。没有 `redact-20` 时，文本模块仍扫目录下全部文件。

## 基础规则

`redact-10-basic.sh` 与 JSON/文本解析无关，直接扫证据目录。解码视图建好之后，`redact-20` 直接调用 `basic_scan_dv` 再补扫一遍（函数不存在就 FAIL），只在有命中时另报，不重复打印 ok；这 14 次计入 `MOD_RULES_20`。

- `sk-` key：左边界是行首，或前一个字符不是字母、数字、`-`（`_` 算边界），或前面是 `\n`、`\t`、`\r`、`\b`、`\f`、`\u00XX`，或百分号编码 `%` 加两位十六进制（`%20sk-…`、`api_key%3Dsk-…` 要报）。`disk-…`、`task-…` 不算。后面至少 16 位 `[A-Za-z0-9_-]`。
- `enc:v1` 密文。
- `eyJ` 开头的 JWT，后面至少 8 位。
- `Bearer` 加分隔符再加至少 8 位凭据字符，不区分大小写。分隔符可以是空白（含制表符）、字面 `\t`、U+00A0、`%20`、`%09`，或 `+`（表单编码的空格）。
- `Authorization` 头的 scheme 形式：Basic、Bearer、Token、ApiKey、Digest（大小写不敏感）。分隔符与 Bearer 相同，不再只认普通空格。允许 JSON 转义引号、值前的 `[`、值后的 `]`。所以 `Authorization: Bearer <redacted>` 和 `Authorization:[Bearer <redacted>]` 不报，换成真值要报。没有方式词的 `Authorization: rawtoken`、`auth: realsecret` 交给 KV。
- `PROBE-HMAC-` 哨兵。
- 探测口令 `probe-` 加 24 位十六进制。
- Cookie / Set-Cookie，包括 JSON 写法 `"set-cookie":`。
- URL 里内嵌的口令（`scheme://user:pw@host`，用户名可以为空，如 `redis://:pw@host`）。口令不含未编码的 `/`，免得路径里带邮箱的 URL 误报。
- 非 `@probe.invalid` 的邮箱。
- `$HOME`、本机主机名。主机名取 `hostname`、它的短名（第一个 `.` 之前）和 `hostname -s`，合成一条规则；取不到 `hostname` 时失败。
- `$USER`：用户名短于 5 个字符，或是 admin、root、user、guest、test、ubuntu、debian、probe、runner、build、deploy、docker、service 这类常见词时，只按 `/home/<u>`、`/Users/<u>` 的路径形态匹配。

## jq 路径上的敏感键

遍历全部字符串叶子。对路径上每个对象键单独匹配，不用 `.` 拼起来的整段路径当一个字符串。字段名含 password、passwd、pwd、secret、token、credential、refresh、api key（`api_key`、`api-key`、`apiKey`）、session_id、sessionid、session-id、hmac、hmac_secret、signature、invite_code、authorization、passphrase，或以 key 结尾。键名里的 `.`、空格、`:` 不影响判断（`db.password`、`Api Key`、`db:password` 都要报）。`auth` 和 `sig` 只认整名（避免 `author`、`design`）。允许前后缀，大小写不敏感。名单只有 `FIELD_CORE` 一份，文本规则和 jq 都从它派生，但两条路径的匹配方式不同：jq 对每个对象键做不锚定匹配；文本规则（含规范化副本和字符串里解析不了的片段）的键名字符类是 `[A-Za-z0-9_-]`，键名里有 `.`、空格、`:` 时认不出。

值的长度 ≥6，且整值不是占位符，就 FAIL。布尔值和数字不是字符串叶子，不报。

`auth`、`authorization` 用路径上**最后一个对象键**判断（数组下标不算）。先去掉开头的方式词（Basic、Bearer、Token、ApiKey、Digest，大小写不敏感）和紧跟着的空白（可以多个空格），再判断剩下的值。方括号包住的整值会先剥掉一层。`{"authorization":["Bearer <redacted>"]}` 和 `Bearer  <redacted>`（两个空格）都不报。

路径：数组下标是数字；`fromjson` 递归出来的对象或数组加一段 `(json)`；各段用 `.` 连接。对象键不是 `[A-Za-z_][A-Za-z0-9_-]{0,31}` 这种普通标识符时，打印成 `<key len=N>`，不打印原文。除此之外，键本身只要像一条值规则——`sk-`、`eyJ` JWT、`probe-` 加 24 位十六进制、长度 ≥16 的十六进制或 base64url——也打印成 `<key len=N>`。30 字符的 `probe-<24hex>` 键因此不会印出原文。

字符串值若能被 `fromjson` 解析成对象或数组，再递归它的叶子。

## 文本规则

`redact-30-text.sh` 对 `TEXT_TARGETS` 跑字段、数组、KV、query、凭据头。每个目标扫两遍：原文一遍，规范化副本再一遍，位置去重。解码视图上的带值规则用 `grep -o` 取出片段，再对片段做占位符锚定，和原文那遍一样。所以 JSON 字符串里的 `?token=<redacted>`、`X-API-Key: <redacted>`、`redis://:<redacted>@host`、`map[X-Api-Key:[<redacted>]]` 不报。

### 敏感字段与数组

键引号可以是 `"` 或 `'`，值的引号也可以和键不同：`"password": 'realsecret1'`、`'password': "it's-realsecret1"` 都要报。字符串值取到与开头相同的结束引号，长度 ≥6，整值是占位符才放过。同一行的数组整段取出 `[…]`（正则在第一个 `]` 处停下）。本行没有 `]` 时取到行尾；`[` 后面什么都没有（键和 `[` 在行尾，值可能在下一行）也算命中，因为看不到值。截断数组里抽出的元素全是占位符或长度 <6，并且 `[` 后不是空的，就不报。元素按引号成对切开，引号里的逗号不当分隔符；反斜杠转义下一个字符。元素可以是双引号、单引号或不带引号。引号没有成对、切分后还有对不上的尾巴，或去掉空白后还有内容却一个元素都抽不出来时，当作命中。去掉占位符和长度 <6 的元素，还剩任何元素就 FAIL。`auth`、`authorization` 的元素先按上面的方式词规则剥离。敏感键后面接着 `{` 时，非空对象要报，内层键不在名单里也报（例如 `request_body={"secret":{"value":"realsecret123"}}`）。空对象 `{}` 不报。本行没有 `}`（对象写在下一行或被截断）时看不到值，也报。数字和布尔值不是字符串，也不报。截断数组、`[` 在行尾、敏感键下的对象这三条只用于非 JSON 文本和解码视图；jq 能解析的 JSON 的规范化副本上，敏感键下的对象和数组已经由 jq 按叶子判过，格式化输出又天然跨行，所以正则不再看这三种形状。

JSON 原文的规范化副本也跑这组正则，所以 `{"password":"firstsecret1","password":"<redacted>"}` 会报出第一个值，并且不打印这个值。

### KV

同一组字段名，含 authorization 和整名 auth。不含 `X-API-Key`、`X-External-User-Token`、`X-Auth-Token` 这三个整名（交给凭据头）。左边界是行首或非 `[A-Za-z0-9_]`，**引号也算边界**。键名后面仍必须是空白加 `[:=]`，所以 `"key":` 这种 JSON 键配不上。

左边界是 `"` 且分隔符是 `:` 时，值截到下一个未转义的 `"`（前面有偶数个反斜杠）。因此 `"password: supersecret1"` 要报，`"password: <redacted>"` 不报。其他值：双引号或单引号按「反斜杠转义下一个字符」解析到结束引号；不带引号时，`key: value` 取到行尾（去掉行尾空白），`key=value` 取到空白、`&`、`;`、`,`、`"` 为止。同一行最多判断 50 处。到上限时这一行还有没看的赋值，整行算命中，规则名后注明「超过单行检查上限」。

`password=\"supersecret1\"` 在规范化副本上变成带引号的赋值，要报。

只豁免证据里的主体标签，不再豁免「`key:` 加 1–6 个字母」。值必须恰好是 `key:empty`、`key:kb1`、`key:noretr`、`key:chat`、`key:full`、`key:mvs`、`key:rev` 之一，区分大小写，不受 `nocasematch` 影响。解码视图（path 模式）只在两种情况下放行：路径最后一个对象键是 `actor`（纯数字的数组下标和 `(json)` 段跳过）；或者这一行里能按 `label_in_actor` 找到 `"actor":"key:标签"`，并且命中的片段落在这个值里面。不再因为「整行等于标签、且这条路径在映射里只出现一次」放行。因此 `{"msg":"{\"actor\":\"key:noretr\"}"}` 不报，`{"note":"key:noretr"}` 和单独的 JSON 字符串 `"key:noretr"` 要报。非解码文本仍放行两种情况：片段在 `"actor":"…"` 的值里，或整行就是这个 JSON 字符串（含 pretty-print 的 `"key:noretr"`）。`key: secret`、`key: "passwd"`、`key:SECRET`、`key: realsecret1`、`key:noretr extra`，以及不在 actor 上下文里的单独一行 `key:noretr`，都要报。

`?` 或 `&` 紧挨键名、分隔符是 `=`、中间没有空格，**并且 query 规则确实匹配这个片段**时，KV 才跳过。匹配不到就照常判断，多报一条也可以。所以 `?token="realsecret1"` 由 KV 报出（引号不在 query 的值字符集里）。

### query 与凭据头

URL query：`?` 或 `&` 后面的参数名与 KV 同一份字段名，紧贴 `=`，值长度 ≥6，值字符不含空白、`"`、`&`、`#`、反斜杠。

`X-API-Key`、`X-External-User-Token`、`X-Auth-Token`：纯文本、JSON 和 Go header map 都查。值取到空白、`,`、`}`、`]`、`"` 为止，允许含 `<` `>`。只有整值恰好是占位符才放过，所以 `map[X-Api-Key:[<redacted>]]` 不报，`X-API-Key: <redacted>realsecret` 要报。

## 规范化的局限

规范化保持 `sed -E 's/\\+"/"/g'`：把一串反斜杠加一个引号收成一个引号。没有另做「奇数个反斜杠 + 引号」的占位保留。

因此值内部的转义引号会变成截断点，正则路径可能漏掉后半段。例如 `{"token":"ab\"cdefghij"}` 会变成 `{"token":"ab"cdefghij"}`，字段正则只看到 `ab`（长度 <6），可以不报。能被 jq 正常解码的字符串叶子仍按解码后的值判断。字符串里如果是 `fromjson` 解析不了的片段（例如截断的 `{"msg":"{\"token\":\"ab\\\"cdefghij\" (truncated"}`），解码视图上只能靠正则。这个局限留在正则路径上：非 JSON 文本、规范化副本，以及字符串里解析不了的 JSON 片段。

## .env 比对

只比对名字里含 SECRET、PASSWORD、`_KEY`、TOKEN、AES、SIGNING 的赋值。解析时去掉 `export` 前缀、引号和行尾注释。空值不比对，也不列为短密钥。1–5 字符的短值不比对，WARN 里列出变量名。都不打印值。长于等于 6 的值若出现在证据里就 FAIL，位置后面只附变量名。解码视图上再用解码后的原值做一次固定串比对，命中位置写成 `文件:JSON路径`，同样只附变量名。空值和短值不参加这一次，也不再打印 WARN。比对用的 grep 退出码 ≥2 即失败。列出变量名的那条 grep 退出码 ≥2 也失败（例如 `.env` 是目录）。名字一条都对不上（例如只有 `OPENAI_APIKEY`）时失败，不记成 0 个值通过。短值和空值仍只告警：名字对得上、但没有长度 ≥6 的值时，可以只有 WARN。没带 `--skip-env` 时，这种情况另打一条 WARN，给出长值、空值、短值各几个，说明这次没有比对任何值。

## 私网地址

规则不查 IP 地址，RFC 1918 私网地址（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）可以出现在证据里。已入库的例子是第 13 项 `13-log-body.txt` 访问日志里的 `client_ip=192.168.97.1`（`4b363d15/`、`cc94ce1b/` 各一处）：这是 compose 默认网桥的网关地址，宿主经 127.0.0.1 发布端口访问 app 时，app 看到的来源就是它。它由 Docker 在本机分配，不对应任何真实主机或局域网设备，也不含凭据，换一台机器重建会变，泄露它不帮助访问任何东西。

出现下面任一情况时，要先加脱敏规则（并重跑已入库的证据），不能再按上面放行：私网地址能对应到真实主机（例如栈改用 host 网络、`network_mode: host`，或日志里出现宿主的局域网地址、VPN 或办公网地址）；地址和端口一起指向能从别处访问的服务；出现公网地址；或者地址与主机名、用户名一起出现，能拼出本机身份。

## 已知局限

- 不在规则范围内：`{"password": 值不带引号}`、`password => "…"`、不带引号的值在空格或 `;` 处截断后不足 6 位、`X-API-Key:` 的值本身再带空格、URL 里再编码一层的 `%3Ftoken%3D…`、`=` 前面是 NBSP。
- 下面几种写法也漏：全角冒号 `password：…`；带引号的键接 `=`，如 `"password"=…`、`"password" = "…"`、`'password'='…'`、`\"password\"=…`（KV 要求键名后直接是空白加 `[:=]`，字段正则要求 `:`）；`=` 本身被百分号编码的 `password%3D…`。
- 文本规则的键名字符类是 `[A-Za-z0-9_-]`。键名带 `.`、空格、`:` 时，非 JSON 文本里的 `log line {"db.password":"…"}`、字符串里解析不了的片段 `{"msg":"{\"db.password\":\"…\" (truncated"}` 都报不出。整文件能解析时，这类键名的重复键由流式遍历看到第一个值。
- 字符串里内嵌的 JSON（`fromjson` 解出来的）有重复键时，仍只看到最后一个值：流式遍历只拆外层，内层由 `fromjson` 解析，会折叠重复键。例如 `{"body":"{\"db.password\":\"…\",\"db.password\":\"<redacted>\"}"}`。
- 只会多报的情况：非 JSON 文本和解码视图里，敏感键下的非空对象一律报，内层值已经是占位符也报（如日志行里的 `{"secret":{"v":"<redacted>"}}`；jq 能解析的 JSON 文件按叶子判，不受影响）。`key: value` 把行尾都当成值，所以占位符后面还有别的头时整行算命中，例如 `map[X-Api-Key:[******] X-Signature:[******]]`、`map[X-Signature:[******] X-Auth-Token:[<redacted>]]` 都会报。
- jq 的解析深度有上限，随版本不同：jq-1.7.1-apple 上纯数组嵌套到 256 层、对象嵌套到 128 层就报 `Exceeds depth limit for parsing`（255、127 层能过）；jq-1.6-apple 上分别是 257 和 129 层。超过上限的文件会被当成非 JSON，只走正则，深层敏感值可能漏报。

跟踪 issue：#25
