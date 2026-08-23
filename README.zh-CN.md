<p align="center">
  <h1 align="center">ProxyDge</h1>
  <p align="center">PROXY Protocol 归一化网关，支持 TCP 和 UDP。</p>
</p>

监听一个端口，接收上游连接/数据报（直连 / PROXY Protocol v1 / v2），将其统一归一化为可配置的 PROXY Protocol 版本，并转发给下游。上游协议形态的差异被本服务吸收。

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/License-GPLv3-blue.svg" alt="License">
  <img src="https://img.shields.io/github/release/MartinZiYun/ProxyDge" alt="Release">
</p>

[English](README.md) | [简体中文](README.zh-CN.md)

## 功能

- **双协议**：TCP（字节流 + 半关闭）和 UDP（数据报 + session 模型）——各有独立网关，不共享流抽象
- **协议归一化**：直连、PROXY v1、PROXY v2 → 统一输出所选版本（`tcp.header-version`），并对自相矛盾的混合地址族头提供显式处置策略
- **IPv4/IPv6 双栈**：完整 IPv6 支持，包括 link-local zone 标识符用于 UDP session 路由
- **来源信任控制**：只有配置的可信 IP 网段才能发送 PROXY header，防止地址伪造。支持 CIDR 和裸 IP（IPv4/IPv6）
- **策略控制**：`use`（默认，三种都收）/ `require`（必须带 PROXY 头）/ `reject`（禁止带 PROXY 头）
- **UDP session 管理**：每 session 独立上游 socket、空闲超时、最大 session 限制、可配置 PROXY header 发射模式
- **配置自动迁移**：新增字段时自动升级旧配置文件，备份原文件，保留未知字段
- **多语言**：`en`（默认）、`zh-CN`（简体中文）、`zh-TW`（繁體中文）
- **单文件部署**：语言文件 `go:embed` 编译进二进制，无外部依赖
- **跨平台**：Linux + Windows × amd64 + arm64

## 快速开始

### 下载

从 [Releases](https://github.com/MartinZiYun/ProxyDge/releases) 页面下载对应平台的二进制文件。

开发版：在 [Actions 运行列表](https://github.com/MartinZiYun/ProxyDge/actions/workflows/push.yml) 打开最近一次成功运行，从 Artifacts 区下载对应平台的二进制文件。

### 配置

```bash
# 生成示例配置
./proxydge init

# 编辑配置文件
vi config.yaml
```

示例配置文件：

```yaml
version: 3  # 不要更改；用于自动迁移

# ── 通用 ───────────────────────────────────────────────────────────
protocol: "tcp"                      # tcp（默认）| udp — 选择网关模式
listen: ":9000"                      # 监听地址 (host:port)
upstream: "127.0.0.1:9001"          # 下游目标 host:port
policy: "use"                        # use | require | reject
lang: ""                             # 显示语言: en|zh-CN|zh-TW（空=自动）

# 信任控制：只有这些网络可以发送 PROXY header
# 支持 CIDR (10.0.0.0/8, 2001:db8::/32) 和裸 IP (10.0.0.1, fe80::1)
# 空（默认）信任所有人 — 生产环境请配置以防伪造
trusted-networks:
  # - "10.0.0.0/8"
  # - "2001:db8::/32"
  # - "10.0.0.1"        # 裸 IP → /32 (IPv4) 或 /128 (IPv6)
untrusted-proxy-action: "reject"     # reject（默认）| strip

# ── TCP (protocol=tcp) ─────────────────────────────────────────────
tcp:
  detect-timeout: "1s"               # PROXY header 检测超时（0=无限等待）
  idle-timeout: "5m"               # 管道空闲超时，0=禁用
  header-version: "v2"               # 下游 PROXY header 版本: v1|v2
  family-mismatch: "reject"          # 地址族不一致处置: reject|unknown|legacy
  max-connections: 4096              # 最大并发连接数, 0=不限制

# ── UDP (protocol=udp) ─────────────────────────────────────────────
# 以下字段仅在 protocol=udp 时生效
udp:
  idle-timeout: "30s"               # UDP session 空闲超时
  max-sessions: 1024                # 最大并发 UDP session 数
  max-datagram-size: 65535          # 最大数据报大小，0=无限制，超限丢弃
  header-mode: every_datagram       # every_datagram（默认）| first_datagram

# ── 日志 ───────────────────────────────────────────────────────────
log:
  console:                          # 日志到 stderr
    level: "info"                    # debug | info | warn | error
    format: "text"                   # text | json
  file:                             # 日志到文件（path 为空=禁用）
    path: ""                         # 如 /var/log/proxydge.log
    level: "info"
    format: "json"
```

### 运行

```bash
./proxydge start
```

启动时会打印版本信息、配置来源和安全警告（如有）。

## CLI 命令

```
proxydge <command> [options]

  start     运行网关
  init      生成示例 config.yaml
  version   打印版本和构建信息
  help      显示帮助
```

直接运行 `./proxydge`（不带参数）等同于 `help`。

### start 选项

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `-protocol <p>` | `tcp` | `tcp` \| `udp` — 选择网关模式 |
| `-listen <addr>` | `:9000` | 监听地址 |
| `-upstream <host:port>` | `127.0.0.1:9001` | 下游目标 |
| `-policy <p>` | `use` | `use` \| `require` \| `reject` |
| `-trusted-networks <cidrs>` | （空=信任全部） | 可信网络，逗号分隔 CIDR 或裸 IP |
| `-untrusted-proxy-action <a>` | `reject` | `reject` \| `strip` |
| `-config <path>` | `<exe-dir>/config.yaml` | 配置文件路径 |
| `-lang <locale>` | 自动检测 | `en` \| `zh-CN` \| `zh-TW` |
| `-tcp-detect-timeout <dur>` | `1s` | PROXY header 检测超时（0=无限等待） |
| `-tcp-idle-timeout <dur>` | `5m` | 管道空闲超时（0=禁用） |
| `-tcp-header-version <v>` | `v2` | 下游 PROXY header 版本: `v1` \| `v2` |
| `-tcp-family-mismatch <a>` | `reject` | 地址族不一致处置: `reject` \| `unknown` \| `legacy` |
| `-tcp-max-connections <n>` | `4096` | 最大并发连接数,超限 accept 直接关闭;`0`=不限制 |
| `-udp-idle-timeout <dur>` | `30s` | UDP session 空闲超时 |
| `-udp-max-sessions <n>` | `1024` | 最大并发 UDP session 数；`0`=不限制 |
| `-udp-max-datagram-size <n>` | `65535` | 最大数据报大小 (0=无限制) |
| `-udp-header-mode <m>` | `every_datagram` | `every_datagram` \| `first_datagram` |
| `-log-console-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-console-format <f>` | `text` | `text` \| `json` |
| `-log-file <path>` | （空=禁用） | 文件日志路径 |
| `-log-file-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-file-format <f>` | `text` | `text` \| `json` |

### init 选项

| 选项 | 说明 |
|------|------|
| `-config <path>` | 示例配置写入位置（默认：`<exe-dir>/config.yaml`）|
| `-force` | 覆盖已存在的配置文件（默认拒绝）|

### version 选项

```bash
./proxydge version           # 详细多行信息
./proxydge version --short   # 仅版本号，如 v0.1.0
```

## 配置

### 优先级

从高到低：

```
CLI flags  >  环境变量 (PROXYDGE_*)  >  配置文件  >  默认值
```

### 环境变量

每个 CLI flag 都有对应的环境变量（前缀 `PROXYDGE_`，大写，`-` 替换为 `_`）。协议专用 flag 使用 `TCP_`/`UDP_` 前缀：

```bash
PROXYDGE_PROTOCOL=udp
PROXYDGE_UPSTREAM=127.0.0.1:9001
PROXYDGE_POLICY=require
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8,192.168.0.0/16
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
PROXYDGE_LANG=zh-CN
PROXYDGE_TCP_DETECT_TIMEOUT=2s
PROXYDGE_TCP_HEADER_VERSION=v1
PROXYDGE_TCP_FAMILY_MISMATCH=unknown
PROXYDGE_TCP_MAX_CONNECTIONS=2048
PROXYDGE_UDP_IDLE_TIMEOUT=60s
PROXYDGE_UDP_MAX_SESSIONS=2048
PROXYDGE_UDP_MAX_DATAGRAM_SIZE=1500
PROXYDGE_UDP_HEADER_MODE=first_datagram
```

### 配置文件发现

1. `-config <path>` 显式指定（文件必须存在，否则报错）
2. 可执行文件旁的 `config.yaml`（自动发现，不存在则静默跳过）

## 来源信任控制

ProxyDge 的 `Policy`（use/require/reject）控制是否接受 PROXY header。**信任控制**控制*谁可以发送*。

### 工作原理

1. 检测连接/数据报是否携带 PROXY header
2. 如果携带 header 且来源 IP **不在可信网络** → 按 `untrusted-proxy-action` 处理：
   - `reject`（默认）：立即关闭连接/丢弃数据报
   - `strip`：剥离 header，用 socket 的真实对端地址重新归一化为直连
3. 然后照常应用策略

**安全关键**：信任检查使用的 IP **始终来自 socket 的真实对端地址**（TCP 的 `RemoteAddr()`，UDP 的 `ReadFromUDP`），而非 PROXY header 中声称的来源地址。攻击者无法通过在 header 中伪造可信 IP 来绕过检查。

### 信任网络配置

`trusted-networks` 同时接受 CIDR 和裸 IP 地址：

```yaml
trusted-networks:
  - "10.0.0.0/8"           # IPv4 CIDR
  - "2001:db8::/32"        # IPv6 CIDR
  - "fe80::/10"            # IPv6 link-local CIDR
  - "10.0.0.1"             # 裸 IPv4 → 自动转为 /32
  - "2001:db8::1"          # 裸 IPv6 → 自动转为 /128
```

### 行为矩阵

| 来源 | 携带 PROXY header | untrusted-proxy-action | 结果 |
|------|-------------------|----------------------|------|
| 可信 IP | 是 | 任意 | Header 可信，正常转发 |
| 可信 IP | 格式错误 | 任意 | 拒绝（信任不绕过协议验证）|
| 不可信 IP | 是 | reject | 连接被拒绝 |
| 不可信 IP | 是 | strip | 剥离 header，用真实 IP 转发 |
| 不可信 IP | 否（直连）| 任意 | 正常转发（信任检查不触发）|

### 启动警告

- `trusted-networks` 为空 → 警告所有来源都被信任，存在伪造风险
- `untrusted-proxy-action=strip` → 警告不可信来源仍可连接
- `tcp.family-mismatch=legacy` → 警告对不一致头沿用历史自动转换（下游可能收到标记为 IPv6 的 `::ffff:` 映射 IPv4 地址）

## TCP 输出版本与地址族不一致处置

### `tcp.header-version` — 归一目标版本

ProxyDge 上游接受直连与 PROXY v1/v2 header,然后把每条连接统一重编码为**一个版本**发给下游:

- `v2`（默认）：二进制 header — 地址族覆盖完整,可扩展
- `v1`：文本 header（`PROXY TCP4 ...` / `PROXY TCP6 ...`）— 供只认旧文本格式的下游服务使用

v1 固有限制：无 TLV 扩展能力;源/目的地址族混合时无法忠实表达。

### `tcp.family-mismatch` — header 自相矛盾时怎么办

PROXY header 用一个地址族字段同时描述源和目的。伪造或出 bug 的上游可以发出"声明 INET6、目的却是 `::ffff:` 映射形式 IPv4"的 header。静默重编码会强转地址——下游可能把 `::ffff:192.168.1.1` 当成真实 IPv6 目标去连接,导致失败或误连。ProxyDge 绝不伪造地址,由你选择处置方式:

| 取值 | 行为 |
|------|------|
| `reject`（默认） | 拨号下游之前直接关闭连接并记日志 |
| `unknown` | 转发无地址 header——v1 输出为 `PROXY UNKNOWN`,v2 输出为 LOCAL+AF_UNSPEC——按协议告知下游走兜底逻辑 |
| `legacy` | 完全跳过检测,逐字节保持历史行为(含静默 `::ffff:` 映射)。选择后启动时打印 WARNING |

检测作用于 trust/policy 处理后的**最终 header**:直连和被 strip 的不可信头都由真实套接字地址重建,天然自洽,永不误伤。

## UDP 网关

当 `protocol: udp` 时，ProxyDge 作为 UDP PROXY Protocol 网关运行，拥有独立的 数据报/session 模型：

- **每 session 独立上游 socket**：每个客户端 session 获得专用 `DialUDP` socket — 内核按来源过滤响应，无需应用层路由表
- **Session 生命周期**：NEW → ACTIVE → EXPIRED（空闲超时）。Session 按 `(来源IP, 来源端口, IPv6 zone)` 区分
- **最大 session 限制**：可配置并发 session 上限（默认 1024，`0`=不限制）；满时新来源的数据报被丢弃
- **PROXY header 发射模式**（`udp.header-mode`）：
  - `every_datagram`（默认）：每个数据报携带 PROXY v2 header — 下游无状态
  - `first_datagram`：仅 session 首包携带 header — 开销更低，需下游维护 flow state
- **输入自动检测**：网关自动检测输入数据报是否携带 PROXY header（direct / first_datagram / every_datagram）— 由 `policy` 字段控制，无需单独的输入配置
- **格式错误 = 丢弃**：如果数据报以 PROXY v2 签名开头但解析失败，直接丢弃 — 绝不回退为普通 payload
- **资源排序**：信任 + 策略决策在 session 创建之前执行 — 被拒绝的来源消耗零资源（无 socket、无 goroutine、无 session 槽位）
- **IPv6 Zone**：不同 zone 的 link-local 地址（如 `fe80::1%eth0` vs `fe80::1%eth1`）被视为不同 session。注意：PROXY Protocol v2 wire 格式不支持 zone 标识符 — zone 在本地用于 session 路由，但不传输到下游

### 配置自动迁移

配置文件包含 `version` 字段标记格式版本。

- **版本相同**：无操作
- **旧版本**：自动迁移 — 备份原文件到 `.bak`，重新生成包含所有字段 + 注释的配置，**逐字保留未知字段**，启动时打印 `NOTICE`
- **新版本**：报错（ProxyDge 可能被降级 — 请升级）
- **缺少 version 字段**：报错（运行 `proxydge init` 生成带版本的配置）

迁移保证**备份成功后才写入原文件**，写入失败不会丢失数据。

`proxydge init` 拒绝覆盖已存在的文件（使用 `-force`）。要升级旧配置，直接运行 `proxydge start` — 会自动迁移。

## 多语言支持

ProxyDge 支持三种显示语言：

| 语言 | 代码 |
|------|------|
| English | `en` |
| 简体中文 | `zh-CN` |
| 繁體中文 | `zh-TW` |

语言选择优先级：

```
--lang flag  >  PROXYDGE_LANG 环境变量  >  系统区域 (LANG/LC_ALL)  >  en
```

```bash
# 方式 1：flag
proxydge start -lang zh-CN
proxydge help -lang zh-TW

# 方式 2：环境变量
PROXYDGE_LANG=zh-CN proxydge start

# 方式 3：系统区域 (Linux)
export LANG=zh_CN.UTF-8
proxydge start
```

不支持的区域设置自动回退到英语。缺少翻译 key 回退到英语；英语也缺少时显示 `[missing:key]` — 永不 panic。

## 架构

```
main.go                                  # 组合根：flag 解析、适配器接线、信号处理
internal/config/                         # 配置加载（defaults < file < env < flags）
internal/protocol/                       # Protocol 枚举 (tcp/udp) — 仅配置/路由标签
internal/transport/                      # 跨传输接口 (AddrConn, Conn, CloseWriter, RemoteIP)
internal/tcp/                            # TCP 专用：Conn (流 + 半关闭), Listener, Dialer
internal/udp/                            # UDP 专用：Gateway, Session, SessionManager (数据报模型)
internal/gateway/                        # TCP 网关 + 共享决策逻辑
  ├── gateway.go                         # Gateway.Serve + handle + Policy
  ├── decide.go                          # Decide() — 信任 + 策略 (TCP/UDP 共享)
  ├── pipe.go                            # pipeStream() — TCP 双向管道
  └── trust.go                           # TrustChecker + UntrustedAction
internal/proxyproto/                     # PROXY Protocol 抽象 (接口 + 类型)
  └── goproxyproto/                      # go-proxyproto 库适配器 (库仅在此导入)
    ├── reader.go                        # TCP 流 reader
    ├── writer.go                        # TCP 流 writer
    └── datagram.go                      # UDP 数据报 reader/writer
internal/i18n/                           # 国际化 (go:embed + locale YAML)
  └── locales/                           # en.yaml / zh-CN.yaml / zh-TW.yaml
internal/version/                        # 版本信息 (ldflags 注入)
```

### 设计原则

- **TCP 和 UDP 拥有独立模型**：UDP 不适配进 `io.ReadWriteCloser`/`pipeStream`。TCP 是字节流 + 半关闭；UDP 是面向消息 + session 生命周期。
- **共享逻辑与传输无关**：`Decide()`（信任 + 策略）、`proxyproto.Header`/`Source`、`transport.RemoteIP` 是共享的。`pipeStream()` 和 `transport.CloseWriter` 是 TCP 专用。
- **库隔离**：业务代码不直接导入 `github.com/pires/go-proxyproto`。库被隔离在 `internal/proxyproto/goproxyproto` 适配器子包内。
- **安全不变量**：信任决策使用真实 socket 对端地址，而非 PROXY header 中声称的来源。Session 元数据深拷贝。不可信 PROXY 元数据永不作为 session 状态持久化。

## 从源码构建

```bash
# 安装依赖
go mod download

# 构建
go build -o proxydge .

# 测试
go test ./...

# 构建发布二进制（需要 GoReleaser）
goreleaser release --snapshot --clean
```

支持平台：`linux/amd64`、`linux/arm64`、`windows/amd64`、`windows/arm64`。

## 许可证

[GPL-3.0 license](LICENSE)
