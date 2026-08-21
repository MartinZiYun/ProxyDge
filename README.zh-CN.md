[English](README.md) | [简体中文](README.zh-CN.md)

# ProxyDge

PROXY Protocol 归一化网关。

监听一个 TCP 端口，接收上游连接（直连 / PROXY Protocol v1 / v2），将其全部归一化为 PROXY Protocol v2，并转发给单一可配置的下游。下游因此永远只收到统一的 v2 头，上游协议形态的差异被本服务吸收。

## 功能

- **协议归一化**：直连、PROXY v1、PROXY v2 → 统一输出 PROXY v2
- **来源信任控制**：只有配置的可信 IP 网段才能发送 PROXY header，防止地址伪造
- **策略控制**：`use`（默认，三种都收）/ `require`（必须带 PROXY 头）/ `reject`（禁止带 PROXY 头）
- **配置自动迁移**：新增字段时自动升级旧配置文件，备份原文件，保留未知字段
- **多语言**：`en`（默认）、`zh-CN`（简体中文）、`zh-TW`（繁體中文）
- **单文件部署**：语言文件 `go:embed` 编译进二进制，无外部依赖
- **跨平台**：Linux + Windows × amd64 + arm64

## 快速开始

### 下载

从 [Releases](https://github.com/MartinZiYun/ProxyDge/releases) 页面下载对应平台的二进制文件。

### 配置

```bash
# 生成示例配置
./proxydge init

# 编辑配置文件（填入 upstream）
vi config.yaml
```

配置文件示例：

```yaml
version: 1

listen: ":9000"                    # 监听地址
upstream: "127.0.0.1:9001"        # 下游目标（必填）
policy: "use"                      # use | require | reject
detect-timeout: "1s"               # PROXY 头探测超时
lang: ""                           # 显示语言: en | zh-CN | zh-TW（空=自动检测）

# 来源信任控制：只有这些网段可以发送 PROXY header
# 空（默认）= 信任所有人——请在生产环境配置以防止伪造
trusted-networks:
  - "10.0.0.0/8"
untrusted-proxy-action: "reject"   # reject（默认，拒绝） | strip（剥离归一化为直连）

log:
  console:
    level: "info"                  # debug | info | warn | error
    format: "text"                 # text | json
  file:
    path: ""                       # 空=禁用文件日志
    level: "info"
    format: "json"
```

### 运行

```bash
./proxydge start
```

启动时打印版本信息、配置来源、安全警告（如有）。

## CLI 命令

```
proxydge <command> [options]

  start     运行网关
  init      生成示例 config.yaml
  version   打印版本与构建信息
  help      显示帮助
```

无参数运行 `./proxydge` 等同于 `help`。

### start 选项

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `-listen <addr>` | `:9000` | 监听地址 |
| `-upstream <host:port>` | （必填） | 下游目标 |
| `-policy <p>` | `use` | `use` \| `require` \| `reject` |
| `-detect-timeout <dur>` | `1s` | PROXY 头探测超时 |
| `-trusted-networks <cidrs>` | （空=全部信任） | 可信网段，逗号分隔 CIDR |
| `-untrusted-proxy-action <a>` | `reject` | `reject` \| `strip` |
| `-config <path>` | `<exe-dir>/config.yaml` | 配置文件路径 |
| `-lang <locale>` | 自动检测 | `en` \| `zh-CN` \| `zh-TW` |
| `-log-console-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-console-format <f>` | `text` | `text` \| `json` |
| `-log-file <path>` | （空=禁用） | 文件日志路径 |
| `-log-file-level <l>` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `-log-file-format <f>` | `json` | `text` \| `json` |

### init 选项

| 选项 | 说明 |
|------|------|
| `-config <path>` | 示例配置写入位置（默认：可执行文件同目录 config.yaml） |
| `-force` | 覆盖已有配置文件（默认拒绝覆盖） |

### version 选项

```bash
./proxydge version           # 详细多行信息
./proxydge version --short   # 仅版本号，如 v0.1.0
```

## 配置

### 配置优先级

从高到低：

```
CLI flags  >  环境变量 (PROXYDGE_*)  >  配置文件  >  默认值
```

### 环境变量

所有 CLI flag 都有对应的环境变量（前缀 `PROXYDGE_`，大写，`-` 替换为 `_`）：

```bash
PROXYDGE_UPSTREAM=127.0.0.1:9001
PROXYDGE_POLICY=require
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8,192.168.0.0/16
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
PROXYDGE_LANG=zh-CN
```

### 配置文件发现

1. `-config <path>` 显式指定（文件必须存在，否则报错）
2. 可执行文件同目录 `config.yaml`（自动发现，不存在则静默跳过）

## 来源信任控制

ProxyDge 的 `Policy`（use/require/reject）控制是否接受 PROXY header，**信任控制**控制谁有权发送。

### 工作原理

1. 检测连接是否带 PROXY header
2. 如果带 header 且来源 IP **不在可信网段** → 根据 `untrusted-proxy-action` 处理：
   - `reject`（默认）：直接关闭连接
   - `strip`：剥离 header，用 TCP 套接字真实 peer address 重新归一化为直连
3. 然后正常应用 policy

**安全关键**：信任检查使用的 IP **始终来自 TCP 套接字的 `RemoteAddr()`**，绝不来自 PROXY header 里声称的源地址。攻击者无法通过在 header 中伪造可信 IP 来绕过检查。

### 行为矩阵

| 来源 | 带 PROXY header | untrusted-proxy-action | 结果 |
|------|----------------|----------------------|------|
| 可信 IP | 是 | 任意 | header 被信任，正常转发 |
| 可信 IP | 畸形 | 任意 | 拒绝（信任不绕过协议校验） |
| 非可信 IP | 是 | reject | 拒绝连接 |
| 非可信 IP | 是 | strip | 剥离 header，用真实 IP 转发 |
| 非可信 IP | 否（直连） | 任意 | 正常转发（信任检查不触发） |

### 启动警告

- `trusted-networks` 为空 → 警告所有来源被信任，有伪造风险
- `untrusted-proxy-action=strip` → 警告非可信来源仍可连接

## 配置自动迁移

配置文件包含 `version` 字段，标记格式版本。

- **版本相同**：不做任何操作
- **版本较旧**：自动迁移——备份原文件到 `.bak`，重新生成含所有字段 + 注释的配置，**未知字段原样保留**，启动时打印 `NOTICE` 提示
- **版本较新**：报错（可能降级了 ProxyDge，请升级）
- **缺少 version 字段**：报错（运行 `proxydge init` 生成带 version 的配置）

迁移保证**备份成功后才写原文件**，不会因写入失败丢数据。

`proxydge init` 拒绝覆盖已有文件（需 `-force`）。旧配置升级请直接运行 `proxydge start`，会自动迁移。

## 多语言支持

ProxyDge 支持三种显示语言：

| 语言 | 代码 |
|------|------|
| English | `en` |
| 简体中文 | `zh-CN` |
| 繁體中文 | `zh-TW` |

语言选择优先级：

```
--lang flag  >  PROXYDGE_LANG 环境变量  >  系统 locale (LANG/LC_ALL)  >  en
```

```bash
# 方式一：flag
proxydge start -lang zh-CN
proxydge help -lang zh-TW

# 方式二：环境变量
PROXYDGE_LANG=zh-CN proxydge start

# 方式三：系统 locale（Linux）
export LANG=zh_CN.UTF-8
proxydge start
```

不支持的 locale 自动 fallback 到 English。缺失翻译 key 时 fallback 到 English，English 也缺失时显示 `[missing:key]`，不 panic。

## 架构

```
main.go                                  # 组合根：解析 flag、装配 adapter、信号处理
internal/config/                         # 配置加载（defaults < file < env < flags）
internal/gateway/                        # 网关：监听循环 + 信任检查 + 归一化 + 双向管道
  ├── gateway.go                         # Gateway.Serve + handle + Policy + TrustChecker
  └── trust.go                           # TrustChecker + UntrustedAction
internal/proxyproto/                     # PROXY Protocol 抽象（接口 + 类型）
  └── goproxyproto/                      # go-proxyproto 库适配器（库仅在此子包 import）
internal/transport/                      # 传输抽象（Conn/Listener/Dialer 接口 + TCP 适配器）
internal/i18n/                           # 多语言（go:embed + locale YAML）
  └── locales/                           # en.yaml / zh-CN.yaml / zh-TW.yaml
internal/version/                        # 版本信息（ldflags 注入）
```

### 库隔离

业务代码不直接 import `github.com/pires/go-proxyproto`。库被挡在 `internal/proxyproto/goproxyproto` 适配器子包后面，换库只改该子包。

## 从源码构建

```bash
# 安装依赖
go mod download

# 构建
go build -o proxydge .

# 测试
go test ./...

# 生成发布二进制（需要 GoReleaser）
goreleaser release --snapshot --clean
```

## License

[GPL-3.0 license](LICENSE)
