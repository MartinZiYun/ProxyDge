---
feature: tcp-header-version
status: delivered
updated: 2026-08-23
branch: feat/tcp-header-version
commits: ffb7199..ffd8b42
---

# TCP Header Version Selection & Family-Mismatch Policy

## Report

**What was built** — TCP 网关新增两个配置字段:`tcp.header-version`(v1|v2,默认 v2)让用户选择归一目标版本——v2 二进制(原行为)或 v1 文本头;`tcp.family-mismatch`(reject|unknown|legacy,默认 reject)处置"header 宣称地址族与实际地址矛盾"的入站头。守卫以 `proxyproto.FamilyMatchesAddrs` 谓词实现(逐地址校验是否满足宣称族,nil 契约显式写死),检测点在 trust/policy 裁决之后、dial 之前:reject 在拨下游前断链记日志;unknown 把头重写为无地址形式(writer 按版本发 `PROXY UNKNOWN` 或 16 字节 LOCAL+AF_UNSPEC 帧);legacy 完全跳过检测,由参数化后的 writer 按库原生语义逐字节复现历史行为(选择时启动打 WARNING)。三路暴露(yaml/env/flag)齐全,i18n 三语言覆盖两个校验错误 key、legacy 警告与 help 文本;配置版本 bump 2→3,迁移对缺失的 family-mismatch 显式注入 legacy 保证升级零行为变化,并顺带补全 v0.3.2 遗留未自动添加的 tcp.idle-timeout。UDP 链路零改动。

**Verification** —
- `go build ./...` PASS;`go vet ./...` PASS
- `go test -count=1 ./...` 全部 9 包 ok,0 失败(config/i18n/gateway/proxyproto/goproxyproto/tcp/udp/version/main)
- gofmt:除仓库既有 CRLF 基线(autocrlf=true,PRE-EXISTING,未触碰的包同样报)外无真实差异
- 真机验收:`-tcp-header-version v1 -tcp-family-mismatch legacy` 启动打印 legacy WARNING 且 Describe 显示两 flag 生效;默认参数仅出现常驻的 trusted-networks 警告;真实代理一条连接,下游回显 `PROXY TCP4 ... HELLO`(线上确为 v1 文本头)
- 关键回归锁定:unknown 走完整链路断言(v1 精确 UNKNOWN 行/v2 精确 16B LOCAL 帧);legacy 双 golden fixture——v2 用改动前捕获的真实 wire hex,v1 用实测修正后的精确文本行

**Journey log**:
- 最初假设只有 v1 输出受混合族影响;临时证据测试(经项目真实 reader→writer 管道 + hex dump)证明当前默认的 v2 输出同样透传 AF_INET6+`::ffff:c0a8:101`,用户"测试没问题"的印象源于 Go 对映射 IP 的透明渲染/拨号容错——据此把守卫定为全版本生效。
- spec 曾断言 legacy+v1 会发出 `::ffff:` 文本;实测 Go 的 `IP.String()` 把 To16 强转结果渲染成裸 IPv4 点分文本,产出 "PROXY TCP6 <v6> <v4字面量>" 自相矛盾行(严格解析器直接拒收)。golden fixture 以实测字节为准,spec 同步修正——读库源码得出的推论必须用 wire 字节验证。
- LOCAL+UNSPEC 帧长度先写 20B 后实测修正为 16B(sig12+ver/cmd1+fam/proto1+len2);凡写进验收的字节级断言都应由测试先行证实。
- 配置版本 bump 后,散落的 `version: 2` 测试夹具静默触发迁移,provenance/log 断言因字段全量物化而翻车;夹具统一升到当前版本,迁移类断言改跟 `currentConfigVersion` 常量而非硬编码。
- Windows PowerShell 下 `-replace` 正则把 Go 字符串里的 `\n` 替换成了双反斜杠损坏源码;对字面替换一律用 `.Replace()` 字符串方法。

## [S1] Problem

两个用户可见问题:

1. **归一目标硬编码**:ProxyDge 的 TCP 网关把所有入站连接(direct / PROXY v1 / PROXY v2)硬编码归一为 PROXY v2 二进制头再发给下游(`goproxyproto/writer.go` 固定 `HeaderProxyFromAddrs(2, ...)`)。只理解 v1 文本头的下游服务无法消费输出。

2. **混合地址族欺骗路径**(字节级实证):当入站 PROXY 头内部 src/dst 地址族矛盾(唯一可穿透形态:声明 AF_INET6 但 dst 为 `::ffff:` 映射的 IPv4),当前管道会静默转换输出——v2 输出原样透传 AF_INET6+`::ffff:c0a8:101`;Go 的渲染层会把强转结果显示为点分十进制从而掩盖矛盾。下游可能把映射地址当作真实 IPv6 目标去连接,导致连接失败或误连——这比诚实告知"地址不可知"更恶劣。

## [S2] Design

### 字段一:`tcp.header-version`(Grill 已定命名)

- `Config.TCPHeaderVersion string`,取值 `"v1" | "v2"`,**默认 `"v2"`**(向后兼容)。
- 三路暴露:YAML `tcp:` 段内 `header-version`;env `PROXYDGE_TCP_HEADER_VERSION`;flag `-tcp-header-version`。
- 注册表条目(TCP 区块)+ 常量 `fTCPHeaderVersion`;校验 switch 返回 `error.invalid_tcp_header_version`。
- 仅作用于 TCP 输出方向;Reader 始终接受 v1+v2 入站;`protocol=udp` 时字段不生效(与其他 `tcp.*` 一致的静默忽略语义)。

### 字段二:`tcp.family-mismatch`(Grill 已定命名与取值)

- `Config.TCPFamilyMismatch string`,取值 `"reject" | "unknown" | "legacy"`,**默认 `"reject"`**(init 样例与 defaultsSource 均为 reject)。
- 三路暴露:YAML `tcp:` 段内 `family-mismatch`;env `PROXYDGE_TCP_FAMILY_MISMATCH`;flag `-tcp-family-mismatch`。
- 校验 switch 返回 `error.invalid_family_mismatch`。

**检测不变式**(纯谓词,放 `proxyproto` 包,可独立单测):命名 `FamilyMatchesAddrs(h Header) bool`。**第一语义:检查的是每个地址*分别*是否满足 header 所宣称的地址族——不是比较两个地址彼此是否同族**(防止实现者写成 `srcV4 != dstV4` 式互比)。谓词契约显式写死 nil 判定:

```text
FamilyTCP4 命中(一致)当且仅当:
    h.SrcIP != nil && h.DstIP != nil
    && h.SrcIP.To4() != nil && h.DstIP.To4() != nil

FamilyTCP6 命中(一致)当且仅当:
    h.SrcIP != nil && h.DstIP != nil
    && h.SrcIP.To4() == nil && h.DstIP.To4() == nil
    && h.SrcIP.To16() != nil && h.DstIP.To16() != nil

FamilyUnspec ⇒ 一致(不声明任何族,无可反驳;writer 对其已有 UNKNOWN/LOCAL
    诚实落点。今日不可达:stream reader 对非 TCP 头直接报错,direct/strip
    头由套接字重建必为 TCP4/TCP6)
其余族(UDP 族等,不会到达 TCP 路径)⇒ 不一致
```

nil 必须显式排除:`nil.To4() == nil`,否则 `FamilyTCP6 + nil + nil` 会假满足"两侧均不可转"分支——这正是 nil 目的形态(v4 源+v6 目的经 reader 归一后 DstIP=nil)必须被抓住的原因,契约不依赖调用方保证非 nil。

典型误例(精炼表述):Family=TCP6 时要求 src 与 dst *各自*为纯 IPv6;dst=`::ffff:` 映射违反的是"dst 不满足 header 宣称的 INET6",而非"src/dst 互不同族"。

**检测点**(管线语义):守卫检查的是**最终要写进下游 PROXY header 的有效地址**,不是原始入站头:

```
Decide()(trust + policy 裁决,strip 时重建为套接字头)
  ↓
得到最终 Effective SrcAddr / DstAddr
  ↓
FamilyMatchesAddrs 判定 → 按 family-mismatch 处置
  ↓
dial downstream
```

untrusted strip 后的头由套接字重建、direct 连接两端同族(OS 保证)——两者数学上自洽,永不误伤。

**动作语义**(作用于 v1/v2 两种输出版本):

| 取值 | 行为 |
|---|---|
| `reject` | 记日志、关闭连接(不拨下游)。干净明确的失败,不给下游任何可误解数据 |
| `unknown` | 头重写为不可知形式:v1 输出 `PROXY UNKNOWN\r\n`;v2 输出 Command=LOCAL + AF_UNSPEC(协议规定的等价诚实形式),下游按契约走兜底逻辑 |
| `legacy` | 跳过检测、零特判,由参数化后的底层编码器按库原生语义处理:v2 输出延续现状的 `To16()`/`To4()` 映射(AF_INET6 + `::ffff:` 透传,与本特性之前逐字节一致);v1 输出走 formatVersion1 原生分支——TCPv6 时 dst 被内部强转 To16(v4 映射形式),Go 的 `IP.String()` 将其渲染为**裸 IPv4 点分文本**,产出 "PROXY TCP6 <v6源> <IPv4字面量>" 的自相矛盾行(严格解析器直接拒收);TCPv4 且目的不可转时返回 ErrInvalidAddress,gateway 走现有写头错误路径(记日志、关连接) |

**地址族语义红线**:除 `family-mismatch=legacy` 明确保留的历史行为外,正常路径(reject、unknown、以及无矛盾头的常规编码)不得通过地址填充、映射或截断改变地址族语义——reject 与 unknown 均不得执行任何此类转换。legacy 是显式选择历史行为的逃生舱,不是默认。

### 配置迁移(版本 bump 2→3)

- `currentConfigVersion` 2 → **3**。
- 老配置文件(version<3)加载时自动迁移:`generateMigratedConfig` 中若源文件无 `family-mismatch`,**显式写入 `family-mismatch: "legacy"`**(保留历史行为,升级不改变运行表现);其余新字段缺省即得默认(header-version→v2)。
- **补全历史欠账**:`tcp.idle-timeout`(v0.3.2 引入)当时未伴随版本 bump,version:2 的老文件从不触发迁移、yaml 里始终缺这一行;本次 2→3 迁移顺带补齐——迁移输出必含 `tcp.idle-timeout`(用户已设则保留,否则默认 `"5m"`)。实现上 `generateMigratedConfig` 已输出该行(config.go 现状),任务验收显式锁定此行为。
- **字段顺序**:迁移输出与 `-init` 样例模板逐行同序;TCP 段顺序为 `detect-timeout`、`idle-timeout`、`header-version`、`family-mismatch`(注册表、sampleConfig、generateMigratedConfig、help 文本四处同序)。
- `-init` 样例与全新手写配置(无该键)→ 默认 `reject`。
- 现有 migration 测试需覆盖:旧版迁移后含显式 legacy 与补全的 idle-timeout;用户已写的值保留。

### 启动提醒(替代原 v1 提醒方案)

- 移除此前设想的 `warning.tcp_header_version.v1`(不再做 v1 选择提醒)。
- 新增:当 `Protocol=="tcp" && TCPFamilyMismatch=="legacy"` 时,`config.Warnings()` 追加 `{Key: "warning.family_mismatch.legacy"}`,经现有 WARNING 标签机制输出 stderr。内容:legacy 保留历史自动转换,含向下游发出 `::ffff:` 映射地址的可能,严格下游可能解析异常或误连,建议 reject/unknown。

### Writer 参数化与不可知编码

- `goproxyproto.writer` 持有 `version byte`;`NewWriter(version byte) pp.Writer`;`WriteTo` 改用 `HeaderProxyFromAddrs(w.version, src, dst)`(库按 Version 分派 formatVersion1/2)。
- 新增:`hdr.Family==FamilyUnspec` 时输出不可知形式——v1 构造 `&gop.Header{Version:1}`(formatVersion1 对 UNSPEC 走 `PROXY UNKNOWN` 短格式);v2 构造 `&gop.Header{Version:2, Command:gop.LOCAL}`(sig+LOCAL+UNSPEC+长度 0)。
- `pp.Writer` 接口形状不变;gateway 保持版本无关,仅日志消息("write v2 header")与包注释措辞通用化。
- main 增加 `tcpHeaderVersion(s) byte` 与 `familyMismatch(s) gateway.FamilyMismatch` 两个映射函数(合法性已由 Validate 保证),gateway.New 相应增参。

### i18n 与文档

- 新增 key ×3(error.invalid_tcp_header_version、error.invalid_family_mismatch、warning.family_mismatch.legacy),en/zh-CN/zh-TW 同步;`TestKeyConsistency`/`TestVerbConsistency` 守护。
- `help.text` 三语言各加两行 `-tcp-header-version`、`-tcp-family-mismatch`(插在 `-tcp-idle-timeout` 之后)。
- README.md / README.zh-CN.md:文档化两字段;family-mismatch 说明三值语义、默认 reject、混合族欺骗背景。迁移升级说明不放 README(用户决定),统一写入 release-notes/v0.4.0.md。

## [S3] Out of Scope

- 配置格式版本 bump 2→3,**仅为引入 `tcp.family-mismatch` 的显式迁移语义**(老配置缺省注入 legacy);不涉及其他字段的迁移映射。
- UDP 链路零改动(DatagramReader/DatagramWriter、udp.* 配置不受影响)。
- 不限制上游接受的版本:Reader 始终同时接受 v1 与 v2 入站。
- 不做逐连接版本协商或自动探测下游能力。
- 不支持 TLV 转发(Header.TLVs 继续保留不填充)。
- 不提供从矛盾头"恢复真实地址"的能力——信息本身不可信,只有 reject/unknown/legacy 三条路。

## Tasks

- [x] T1: config 包双字段全链路(Config 字段、注册表+常量、defaultsSource(reject/v2)、yaml/env/flag 三源、Validate、sampleConfig、currentConfigVersion→3、generateMigratedConfig 缺省注入 legacy)+ 三个 i18n key 三语言翻译 + Warnings() legacy 条件 — acceptance: `go test ./internal/config ./internal/i18n` 通过;非法值返回对应 ConfigError key;迁移测试断言旧版配置产出显式 `family-mismatch: "legacy"` 且用户已有值保留;迁移输出含补全的 `tcp.idle-timeout`(缺省 `"5m"`、已设保留)且全部字段顺序与 `-init` 模板逐行同序;Warnings 单测覆盖 legacy 出现/其余不出现 (covers: S2)
- [x] T2: goproxyproto writer 参数化(NewWriter(version byte))+ FamilyUnspec 编码(v1→PROXY UNKNOWN,v2→LOCAL+UNSPEC)— acceptance: 单测断言 v1 文本头/v2 二进制签名/UNKNOWN 短格式;**v2 LOCAL 帧断言完整 wire 字节**:sig + ver/cmd=0x20(LOCAL) + fam/proto=0x00(UNSPEC) + length=0x0000,共 **16 字节**(sig12+ver/cmd1+fam/proto1+len2);混合族(TCP4 头+v6 目的)v1/v2 均返回 ErrInvalidAddress;`go test ./internal/proxyproto/...` 通过 (covers: S2; depends: 无,可与 T1 并行)
- [x] T3: proxyproto.FamilyMatchesAddrs 谓词(实现带注释说明"每个地址分别是否满足 header 宣称族"的判定语义与 nil 契约)+ gateway 集成(New 增参、post-Decide 检测、reject 关连接日志、unknown 重写为 Unspec 头)— acceptance: 谓词单测覆盖四族形态**含 nil 形态**(`FamilyTCP6+nil+nil` 不得假满足、`FamilyTCP4+DstIP=nil` 必须判不一致);**unknown 断言走完整链路**——mixed 入站经 gateway(family-mismatch=unknown)→writer→最终 wire 为 UNKNOWN/LOCAL 帧,而非仅单测 writer 的 Unspec 编码能力;gateway 测试断言 reject 断链;legacy 双 golden fixture:**v2+legacy 断言与改动前捕获的真实 wire 字节逐字节一致**(混合头 AF_INET6+映射 dst 的完整帧 hex,非仅"可解析"),**v1+legacy 断言符合 formatVersion1 原生分支语义**(TCPv6→dst 经 To16 强转后由 Go 渲染为裸 IPv4 点分文本的精确行——实测修正,非 ::ffff: 文本;TCPv4+不可转 dst→ErrInvalidAddress 错误路径关连接);formatVersion1 结论已对照当前锁定依赖 github.com/pires/go-proxyproto **v0.7.0**(go.mod 唯一版本,无 replace)源码核实 (covers: S2; depends: T1, T2)
- [x] T4: main 装配(tcpHeaderVersion/familyMismatch 映射、NewWriter 传版本、gateway 增参)+ gateway 日志消息与包注释通用化 + proxyproto.Writer 注释更新 — acceptance: 本地真实运行 `-tcp-header-version v1 -tcp-family-mismatch legacy` 打印 WARNING 且正常代理;默认参数无 WARNING;`go build ./...` 通过 (covers: S2; depends: T2, T3)
- [x] T5: help.text 三语言新增两行 flag 说明 + README.md / README.zh-CN.md 双字段文档 — acceptance: `go test ./internal/i18n` 通过;两份 README 含 tcp.header-version、tcp.family-mismatch;迁移注记位于 release-notes/v0.4.0.md(评审定稿位置,README 不含) (covers: S2; depends: T1)
- [x] T6: 全量验证 — acceptance: `go build ./...`、`go vet ./...`、`go test ./...` 全绿,gofmt 无差异 (covers: S2; depends: T4, T5)
