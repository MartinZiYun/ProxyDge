---
feature: source-trust-control
status: delivered
specs:
  - docs/compose/specs/2026-01-20-source-trust-control-design.md
plans:
  - docs/compose/plans/2026-01-20-source-trust-control.md
branch: feat/source-trust-control
commits: 00e3101..264b565
---

# Source Trust Control — Final Report

## What Was Built

ProxyDge 新增了**来源信任控制层**：只有来自配置的可信 IP 网段的连接，其 PROXY Protocol header 才被信任并转发给下游。非可信来源带 PROXY header 时的行为可配置——直接拒绝（`reject`，默认）或剥离 header 并用 TCP 套接字真实 peer address 重新归一化为直连（`strip`）。

此功能填补了原有 `Policy`（use/require/reject）的安全缺口：旧 Policy 只控制"是否接受 PROXY header"，但不控制"谁有权发送"——任何 IP 都可以伪造 header 声称来自任意地址。现在两个概念正交分离：trust 回答"谁可以发"，policy 回答"是否接受"。

空 `trusted-networks`（默认）= 信任所有人，向后兼容。安全功能是 opt-in：在生产环境配置可信网段即可启用。

## Architecture

### 流水线

信任检查位于 `reader.Read` 之后、现有 policy 之前：

```
accept c → reader.Read → [trust check] → [policy check] → dial downstream → pipe
```

信任检查块（`internal/gateway/gateway.go` `handle` 方法）：
- 仅当 `src != SourceDirect`（检测到 PROXY header）且 `!trust.IsTrusted(remoteIP)` 时触发。
- `remoteIP` 取自 `c.RemoteAddr()`（TCP 套接字真实 peer），**绝不**取自 PROXY header 里声称的 SrcIP。
- `reject` → 关闭连接、记日志、返回。
- `strip` → 设 `src = SourceDirect`、`hdr = HeaderFromConn(c)`（用真实地址重建 canonical header），然后继续走现有 policy 流程。

> **Policy 评估时序**：Policy evaluates the normalized source **after** trust handling, not the raw presence of a PROXY header。即 `policy=reject` 不是"只要出现过 PROXY header 就拒绝"，而是"经过 trust normalization 后，如果 source 仍然是 PROXY，才 reject"。

### 组件

| 文件 | 职责 |
|------|------|
| `internal/gateway/trust.go` | `UntrustedAction` 枚举（reject/strip）+ `TrustChecker` 类型 + `NewTrustChecker` + `IsTrusted` |
| `internal/gateway/gateway.go` | Gateway struct 增 `trust`/`untrusted` 字段，`handle` 增信任检查块，`remoteIP` helper |
| `internal/config/config.go` | Config 增 `TrustedNetworks`/`UntrustedProxyAction` 字段，四 Source 叠加，`parseCIDRList`，`Validate`，`Warnings`，sample 模板 |
| `main.go` | 构造 `TrustChecker`、`untrustedProxyAction` 映射、注入 `gateway.New`、打印 `Warnings`、help 文本 |

### 设计决策

- **Trust check 在探测之后而非之前**：reader.Read 已消费 PROXY header 字节。如果跳过探测，这些字节会作为应用数据透传给下游——下游会看到 `[v2头][PROXY TCP4 fake.ip...][app data]`。探测后消费 + 丢弃 header 字节是正确的。
- **remoteIP 始终来自套接字**：攻击者可在 PROXY header 里填可信 IP 绕过检查。trust 判断必须用 `c.RemoteAddr()` 的真实 TCP peer address。有专门测试覆盖此场景。
- **空列表 = 信任所有人**：向后兼容。不配置 = 功能不启用。启动时打 WARNING 提醒后果。
- **`untrusted-proxy-action` 而非 `untrusted-action`**：命名明确这是关于 PROXY header 的行为控制，避免与现有 `policy` 混淆。

## Usage

### 配置

```yaml
# config.yaml
trusted-networks:
  - "10.0.0.0/8"
  - "192.168.1.0/24"
untrusted-proxy-action: "reject"   # reject (default) | strip
```

CLI：
```bash
proxydge start -upstream 127.0.0.1:9001 \
  -trusted-networks "10.0.0.0/8, 192.168.1.0/24" \
  -untrusted-proxy-action reject
```

ENV：
```bash
PROXYDGE_TRUSTED_NETWORKS=10.0.0.0/8,192.168.1.0/24
PROXYDGE_UNTRUSTED_PROXY_ACTION=reject
```

优先级：flags > env > file > defaults。CLI/ENV 用逗号分隔 CIDR，解析时自动 TrimSpace 并跳过空串。

### 行为矩阵

| 来源 | 带 PROXY header | untrusted-proxy-action | 结果 |
|------|----------------|----------------------|------|
| 可信 IP | 是 | 任意 | header 被信任，正常转发 |
| 可信 IP | 畸形 | 任意 | 拒绝（trust 不绕过协议格式校验） |
| 非可信 IP | 是 | reject | 拒绝连接 |
| 非可信 IP | 是 | strip | 剥离 header，用真实 IP 转发 |
| 非可信 IP | 否（直连） | 任意 | 正常转发（trust 不触发） |

### 启动警告

- `trusted-networks` 为空 → 警告所有来源被信任，有伪造风险。
- `untrusted-proxy-action=strip` → 警告非可信来源仍可连接。

## Verification

```
go build ./... → PASS
go vet ./...  → PASS
go test ./... -count=1 → PASS (91 tests, 0 failures)
```

新增 28 个测试：
- **TrustChecker 单元测试**（7）：nil → true、空列表 → true、单/多 CIDR 匹配、IPv4-mapped IPv6、非法 CIDR 报错、String() 方法。
- **Gateway 集成测试**（6）：非可信 reject、非可信 strip（验证 SrcIP 是真实 127.0.0.1 而非伪造的 192.0.2.1）、非可信直连不受影响、非可信 header 声称来自可信网段仍被拒绝（安全关键测试）、可信 IP 正常转发、可信 IP 畸形 header 仍被拒绝。
- **Config 测试**（12）：校验（非法 action/非法 CIDR）、默认值、CSV 解析（TrimSpace/空值/尾随逗号）、ENV 解析、provenance 追踪、Warnings（4 种组合）。
- **main 测试**（3）：非法 action 退出码 2、合法 action/strip 退出码 1、合法 trusted-networks 退出码 1。启动横幅验证 WARNING 文本正确输出。

## Journey Log

- [lesson] trust check 必须在 reader.Read 之后：跳过探测会导致 PROXY header 字节作为应用数据透传给下游，破坏归一化语义。
- [lesson] remoteIP 必须来自 TCP 套接字而非 PROXY header：否则攻击者可在 header 里填可信 IP 绕过检查。有专门测试覆盖。
- [lesson] 既有 config 测试需要补上新的必填字段（UntrustedProxyAction），否则 Validate 在空值上报错。
- [lesson] Policy 评估的是 normalized source after trust handling，不是 raw PROXY header 的存在——需要在代码注释和 spec 中钉死此语义，防止未来重构时误传 raw src。

## Source Materials

| File | Role | Notes |
|------|------|-------|
| `docs/compose/specs/2026-01-20-source-trust-control-design.md` | 设计文档 | 完整设计（S1-S7），含流水线、配置、代码结构、测试计划 |
| `docs/compose/plans/2026-01-20-source-trust-control.md` | 实现计划 | 4 个 TDD task，逐步骤代码 |
