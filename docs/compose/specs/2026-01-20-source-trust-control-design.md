---
feature: source-trust-control
status: draft
branch: feat/source-trust-control
---

# Source Trust Control for PROXY Protocol Headers

## [S1] Problem

ProxyDge 当前的 `Policy`（use/require/reject）只控制**是否接受** PROXY header，但不控制**谁有权发送**。任何 IP 连上来都可以发一个伪造的 PROXY v1/v2 header，声称自己来自 `127.0.0.1` 或内网任意地址。这会导致下游错误信任攻击者声明的源地址，从而产生源地址伪造风险。

需要一个**来源信任控制层**：只有来自配置的可信 IP 段的连接，其 PROXY header 才被信任并转发。非可信来源带 PROXY header 时的行为可配置（拒绝或剥离归一化为直连）。

## [S2] Design overview

信任检查是 reader.Read 之后、现有 policy 之前的一个新层。两个概念正交分离：

- **`policy`**（现有，use/require/reject）—— 回答"是否接受 PROXY header"。
- **`untrusted-proxy-action`**（新增，reject/strip）—— 回答"非可信来源带 PROXY header 时怎么做"。

信任判断用的 IP **始终来自 TCP 套接字的真实 `RemoteAddr()`**，绝不来自 PROXY header 里声称的 SrcIP。否则攻击者可在 header 里填可信 IP 绕过检查。

**默认 trusted-networks 为空 = 信任所有人**（向后兼容，安全功能 opt-in）。

## [S3] Pipeline

```
accept c
br := bufio.NewReader(c)
hdr, src, err := reader.Read(br)               // 1. 探测（不变）
if err != nil: close (畸形)

// 2. 信任检查（新增）
if src != SourceDirect && !trust.IsTrusted(remoteIP(c)):
    switch untrustedProxyAction:
    case reject: close(c); return                // 默认：直接拒绝
    case strip:  src = SourceDirect
                 hdr = HeaderFromConn(c)         // 用真实 TCP peer address 重建 canonical header

// 3. 现有 policy（不变）
switch {
case policy==reject  && src != SourceDirect: close; return
case policy==require && src == SourceDirect: close; return
case src == SourceDirect: hdr = HeaderFromConn(c)
}
```

`remoteIP(c)` 从 `c.RemoteAddr()` 类型断言为 `*net.TCPAddr` 取 `.IP`。Trust check 用的 IP **始终来自套接字**，不是 PROXY header 的 SrcIP。

**PROXY header 字节已由 reader.Read 消费**——strip 时不转发 header 字节，只转发剩余应用数据。reject 时直接关闭，header 字节随连接关闭丢弃。不存在 header 字节泄漏到下游应用数据的问题。

### Policy 交互矩阵（非可信 IP 带 PROXY header）

| untrusted-proxy-action | policy | 结果 |
|------------------------|--------|------|
| reject | 任意 | 拒绝连接 |
| strip | use | 剥离→直连→真实 IP 转发 |
| strip | require | 剥离→直连→require 拒绝直连 |
| strip | reject | 剥离→直连→reject 允许直连（header 已剥离，无伪造） |

可信 IP 和直连连接不受 trust 层影响，行为与现有完全一致。

> **Policy 评估时序**：Policy evaluates the normalized source **after** trust handling, not the raw presence of a PROXY header. 即 `policy=reject` 不是"只要客户端原始请求里出现过 PROXY header 就拒绝"，而是"经过 trust normalization 后，如果 source 仍然是 PROXY，才 reject"。重构 pipeline 时切勿将 `raw src != Direct` 错误地传递到 policy 阶段。

## [S4] Configuration

新增两个配置字段。遵循现有 Source 叠加模型（defaults < file < env < flags）。

### 字段

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `trusted-networks` | `[]string` (CIDR) | `[]`（空=信任所有人） | 允许发送 PROXY header 的可信网段 |
| `untrusted-proxy-action` | `string` | `"reject"` | 非可信来源带 header 时：reject \| strip |

### 三源格式

**YAML（file source）：**
```yaml
trusted-networks:
  - "10.0.0.0/8"
  - "192.168.1.0/24"
untrusted-proxy-action: "reject"
```

**CLI flags（flag source）：**
```
-trusted-networks "10.0.0.0/8, 192.168.1.0/24"
-untrusted-proxy-action reject
```

**ENV（env source）：**
```
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8, 192.168.1.0/24
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
```

### CSV 解析规则

CLI 和 ENV 的 `trusted-networks` 是逗号分隔字符串。解析时对每项做 `strings.TrimSpace` 并跳过空串：

```go
func parseCIDRList(s string) []string {
    var out []string
    for _, p := range strings.Split(s, ",") {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}
```

这确保 `"10.0.0.0/8, 192.168.1.0/24"`（带空格）和 `"10.0.0.0/8,192.168.1.0/24,"`（尾随逗号）都正确解析。YAML list 格式天然无此问题。

### 校验（Config.Validate）

- `untrusted-proxy-action` 必须是 `reject` 或 `strip`，否则报错。
- 每个 `trusted-networks` 条目必须能 `net.ParseCIDR`，否则报错。
- 空 `trusted-networks` 合法（= 信任所有人，不报错，但启动时打警告）。

### 启动安全警告

`cmdStart` 在启动横幅之后打印 `Config.Warnings()` 返回的警告：

- **trusted-networks 为空**：`"trusted-networks is empty: all sources are trusted. Any IP can spoof source addresses via PROXY headers. Configure trusted-networks in production."`
- **untrusted-proxy-action=strip**：`"untrusted-proxy-action=strip: non-trusted sources with PROXY headers will have their headers stripped and forwarded with real socket addresses. They can still connect — use reject (default) to deny them."`

### Sample config（`proxydge init` 模板追加）

```yaml
# Trust control: only these networks may send PROXY headers.
# Empty (default) trusts everyone — configure in production to prevent spoofing.
trusted-networks:
  # - "10.0.0.0/8"
  # - "192.168.1.0/24"
untrusted-proxy-action: "reject"   # reject (default) | strip
```

## [S5] Code structure

### `internal/gateway` — TrustChecker + UntrustedAction

```go
// UntrustedAction is what the gateway does when a non-trusted source
// sends a PROXY header.
type UntrustedAction int

const (
    UntrustedReject UntrustedAction = iota // default: close the connection
    UntrustedStrip                          // strip header, re-normalize as direct
)

func (a UntrustedAction) String() string // "reject" | "strip"

// TrustChecker tests whether a remote IP is allowed to send PROXY headers.
// nil TrustChecker or empty trusted list = trust everyone (opt-in security).
type TrustChecker struct {
    nets []*net.IPNet
    all  bool // true when no networks configured
}

func NewTrustChecker(cidrs []string) (*TrustChecker, error)
func (t *TrustChecker) IsTrusted(ip net.IP) bool
```

`IsTrusted` 语义：
- `t == nil` → true（nil = 信任所有人）。
- `t.all == true`（空列表）→ true。
- 遍历 `t.nets`，任一 `Contains(ip)` → true。
- 都不命中 → false。

### `internal/gateway` — Gateway 变更

`Gateway` struct 增加两个字段：`trust *TrustChecker` 和 `untrusted UntrustedAction`。

`New` 签名增加两个参数（追加在现有参数之后）：

```go
func New(ln transport.Listener, dialer transport.Dialer, r proxyproto.Reader,
    w proxyproto.Writer, policy Policy, upstream string,
    detectTimeout time.Duration, logger *slog.Logger,
    trust *TrustChecker, untrusted UntrustedAction) *Gateway
```

`handle` 方法在 reader.Read 之后、现有 policy switch 之前插入信任检查块（见 S3 pipeline）。

### `internal/config` — Config 变更

Config struct 增加两个字段：

```go
TrustedNetworks     []string
UntrustedProxyAction string
```

- `configFields` 追加两条（`trusted-networks`、`untrusted-proxy-action`）。
- `fieldName` 常量追加 `fTrustedNetworks`、`fUntrustedProxyAction`。
- `defaultsSource`：`UntrustedProxyAction = "reject"`，`TrustedNetworks = nil`。
- `fileSource`：YAML `trusted-networks`（`[]*string`）和 `untrusted-proxy-action`（`*string`）用指针判 presence。
- `envSource`：`PROXYDGE_TRUSTED_NETWORKS`（CSV 解析）、`PROXYDGE_UNTRUSTED_PROXY_ACTION`。
- `flagSource`：`-trusted-networks`（string，CSV 解析）、`-untrusted-proxy-action`（string）。
- `Validate`：校验 `untrusted-proxy-action` 值 + 每个 CIDR。
- `Warnings() []string`：返回安全警告列表。
- `WriteSample` / `sampleConfig`：追加模板。

### `main.go` — 组合根

`cmdStart`：
- 构造 `TrustChecker`：`gateway.NewTrustChecker(cfg.TrustedNetworks)`。
- 映射 `untrustedProxyAction(cfg.UntrustedProxyAction)` → `gateway.UntrustedAction`。
- `gateway.New(...)` 传入 trust 和 untrusted。
- 横幅后打印 `cfg.Warnings()`。

```go
func untrustedProxyAction(s string) gateway.UntrustedAction {
    if s == "strip" {
        return gateway.UntrustedStrip
    }
    return gateway.UntrustedReject
}
```

### `internal/transport` — 无变更

`Conn` 接口已有 `RemoteAddr() net.Addr`，无需改动。

## [S6] Testing

### TrustChecker 单元测试（`internal/gateway/trust_test.go`）

- `nil TrustChecker → IsTrusted(any) == true`（nil-safety）。
- 空 CIDR 列表 → `IsTrusted` 对任意 IP true。
- 单个 CIDR → 区内 IP true、区外 IP false。
- 多个 CIDR → 任一命中 true、都不命中 false。
- IPv4-mapped IPv6（`::ffff:10.0.0.1` 对 `10.0.0.0/8`）正确匹配。
- `NewTrustChecker` 非法 CIDR → 返回 error。

### Gateway 集成测试（`internal/gateway/gateway_test.go` 追加）

新增 `startGatewayTrusted` helper（传非空 TrustChecker + UntrustedAction）。复用现有 `startDownstream`。

- **非可信 IP + PROXY v2 header + untrusted=reject** → 连接被关闭，下游不收到数据。
- **非可信 IP + PROXY v2 header + untrusted=strip** → 下游收到 v2 header（SrcIP = 真实 client IP，不是 header 声称的伪造 IP）+ payload。
- **非可信 IP + 直连（无 header）+ untrusted=reject** → 正常转发（trust 检查不触发，直连不受 trust 控制）。
- **非可信 IP + PROXY header 声称来自可信网段 + untrusted=reject** → 仍然拒绝（remoteIP 来自套接字，不是 header）。
- **可信 IP + PROXY v2 header** → 正常转发（行为不变）。
- **可信 IP + 畸形 PROXY header** → 仍然拒绝（trust 只解决"谁可以发"，不绕过协议格式校验；pipeline 中 reader.Read 返回 err → close，在 trust 检查之前）。

### Config 测试（`internal/config` 追加）

- `untrusted-proxy-action` 校验：非法值报错。
- `trusted-networks` CIDR 校验：非法 CIDR 报错。
- CSV 解析 + TrimSpace：`"10.0.0.0/8, 192.168.1.0/24"` → 两条无空格残留。
- CSV 空值：`""`、`"10.0.0.0/8,"`（尾随逗号）→ 正确跳过空串。
- provenance：`trusted-networks` 和 `untrusted-proxy-action` 出现在 `Describe()`。
- `Warnings()`：空 trusted-networks → 1 条警告；`untrusted-proxy-action=strip` → 1 条警告；两者都有 → 2 条；都安全 → 0 条。

### main 测试

- 启动横幅后 `Warnings()` 内容打印到 stderr（空 trusted-networks 时可见警告文本）。

### 现有测试不受影响

现有 6 个 gateway 测试用 `startGateway`（传 nil trust + UntrustedReject）。nil trust = 信任所有人 → 信任检查恒 true → 不触发 untrusted action → 行为不变。

## [S7] Out of scope

- **TLS / mTLS**：不做传输层鉴权。
- **动态 trust 列表 / 热重载**：配置变更需重启。
- **per-IP 限流 / 统计**：不做频率控制。
- **trust 日志审计**：仅标准 accept/reject 日志行，不做单独审计流水。
- **WebSocket / HTTP 层 trust**：纯 TCP 透传，不解析应用层。
