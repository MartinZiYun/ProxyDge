---
feature: proxy-protocol-normalizer
status: delivered
updated: 2026-08-20
branch: feat/proxy-protocol-normalizer
commits: 99b5be5..37cb8a6
---

# Proxy Protocol Normalizer (ProxyDge)

## Report

**What was built** — ProxyDge 是一个 PROXY Protocol 归一化网关（Go，module `proxydge`）。监听一个 TCP 端口，接收上游连接（直连 / PROXY v1 / PROXY v2），统一归一化为 PROXY Protocol v2，连单一可配置下游、先写 v2 头、再双向透传（含 TCP 半关闭）。go-proxyproto 库被挡在自有抽象后面：`internal/proxyproto`（`Header`/`TLV`/`Family`/`Source`/`AddrConn` + `Reader`/`Writer` 接口 + `HeaderFromConn`）、`internal/transport`（`Conn`/`Listener`/`Dialer` 接口 + TCP 适配器）、`internal/gateway`（监听循环 + 每连接处理器 + policy），仅 `internal/proxyproto/goproxyproto` 适配器子包 import 库——换库只改该子包。policy（`use`/`require`/`reject`，默认 `use`）回答「谁允许发 PROXY Header」。探测委托 `proxyproto.Read`（库自带 Peek(1) 短路直连），`ErrNoProxyProtocol`→直连不消费字节；畸形→关闭不回退。`main.go` 为组合根：`-listen`/`-upstream`/`-policy`/`-detect-timeout` flag + 适配器/policy/detectTimeout 注入 + SIGINT/SIGTERM 优雅关闭 + 退出码（用法错误 2、运行错误 1）。

**Verification** —
- `go build ./...` → PASS（exit 0）
- `go vet ./...` → PASS（exit 0）
- `go test -count=1 ./...` → PASS，19 个测试横跨 5 个包（proxyproto / goproxyproto / transport / gateway / main）
- 真实二进制 v2 端到端 smoke → PASS（下游收到 32 字节 = 28 字节 v2 头 + "PING"，回声回到客户端）
- 子代理审查（general-1）覆盖 `99b5be5..c13fba1`：5 个任务全部 MET、无 critical；partial-candidate-timeout 死锁风险经实证反驳（Peek 字节保留、转发无丢字、无提前下游 FIN）。
- 审查后 nit 清理 commit `37cb8a6`（机械拆分 reader/writer 文件 + 删除审查者遗留的一次性 `cmd/` probe + 校准 spec 传输说明；无逻辑改动）已 re-verify（build/vet/19 测试 PASS）。

**Journey log** —
1. **库隔离抽象先行**：按用户硬性要求，把 go-proxyproto 挡在 `internal/proxyproto/goproxyproto` 后面，业务代码只依赖 `proxyproto`/`transport` 接口。用最小 `AddrConn` 接口（仅 LocalAddr/RemoteAddr）避免 `proxyproto`→`transport` import 环。
2. **v2 签名记错被 oracle 捕获**：第一版把 PROXY v2 12 字节签名写成 `...5154585450`（"QTXTP"），实际是 `...515549540a`（"QUIT\n"）。T2 的「按规范手算字节做 oracle、不调库」测试立即失败并定位——oracle 独立于库的价值在此兑现。
3. **Peek(6) 不阻塞→委托库的 Peek(1)**：用户指出首字节即可排除 PROXY、不应为直连流量引入多余阻塞。查 go-proxyproto 源码发现其 `Read` 注释明确「为小包非 PROXY 流量先只 Peek 1 字节」——委托库即满足，无需自写探测。
4. **候选前缀不完整的真死锁**：直连客户端发 `'P'` 开头短命令后等响应时，库 `Peek(5)` 无限阻塞。`gop.ReadTimeout` 会留遗弃 goroutine 操作 `bufio.Reader`（并发不安全）。改用 `SetReadDeadline(now+detectTimeout)` 包住 `reader.Read`、返回后清零，让 `Peek` 本身在 deadline 返回；adapter 把超时映射为 `SourceDirect`（Peek 不消费、字节随管道转发）。无遗留 goroutine、无丢字。
5. **直连用真实地址、绝不 UNSPEC**：归一化的目的就是保住客户端真实信息，直连时套接字 RemoteAddr/LocalAddr 即真实信息；UNSPEC 仅用于「无法判定」，本设计不产生该情况。

## [S1] Problem

需要一个常驻网络服务：在一个 TCP 端口上监听上游连接。上游发来的连接可能是三种之一：

1. **直连**（不带 PROXY Protocol 头，直接是应用层数据）；
2. **PROXY Protocol v1**（文本头，`PROXY <fam> <src> <dst> <sport> <dport>\r\n`）；
3. **PROXY Protocol v2**（二进制头，12 字节签名 + 版本/命令 + 地址族/传输 + 长度 + 地址块）。

无论上游是哪种，本服务都要把连接**归一化为 PROXY Protocol v2**，向**单一可配置的下游**建立 TCP 连接，先写 v2 头，再把双向流量透传（含半关闭语义）。下游因此永远只收到统一的 v2 头，上游协议形态的差异被本服务吸收。

## [S2] Design

### 配置

- CLI flag `-listen <addr>`：监听地址，如 `:9000` 或 `0.0.0.0:9000`，默认 `:9000`。
- CLI flag `-upstream <host:port>`：下游目标，**必填**，缺省则启动报错退出码 2。
- CLI flag `-policy <use|require|reject>`：上游 PROXY 头策略，默认 `use`（见「探测逻辑与 policy」）。
- CLI flag `-detect-timeout <duration>`：PROXY 头探测超时，默认 `1s`。候选前缀（首字节 `'P'`/`'\r'`）已到但完整签名未在此时限内到达 → 按直连处理（已 Peek 字节保留、随管道转发）。解开「候选前缀不完整且对端不再发字节」的死锁（见「探测逻辑与 policy」）。
- 仅 TCP。日志打到 stderr，每连接一行：accept（含检测到的来源类型与 policy 判定）、upstream 拨号失败/成功、转发结束原因。

### 架构：库隔离抽象（用户硬性要求）

业务代码**不得直接 import `github.com/pires/go-proxyproto`**。库被挡在自有的抽象后面，以便将来替换库时只改 adapter 子包、不碰 Gateway。

包布局（module `proxydge`）：

```
main.go                                  # 组合根：解析 flag、装配 adapter 与 policy、跑 Gateway、信号处理
internal/proxyproto/proxyproto.go        # 自有 Header/TLV 类型 + Reader/Writer 接口 + HeaderFromConn
internal/proxyproto/goproxyproto/        # 适配器：用 go-proxyproto 实现 proxyproto.Reader/Writer
internal/transport/transport.go          # 自有 Conn/Listener/Dialer 接口 + TCP 适配器
internal/gateway/gateway.go             # 监听循环 + 每连接处理器 + policy 落地，只依赖上述接口
internal/gateway/gateway_test.go        # 集成测试（真实 proxyproto adapter + 假下游记录器 + 假 transport）
```

`internal/proxyproto`（协议抽象，零第三方依赖）：

- `type Family int`：`FamilyUnspec`、`FamilyTCP4`、`FamilyTCP6`。
- `type TLV struct { Type byte; Value []byte }`：第一版解析不填充、序列化不输出，**架构预留**，未来加 TLV forwarding 不必改 Header 形状。
- `type Header struct { SrcIP, DstIP net.IP; SrcPort, DstPort uint16; Family Family; TLVs []TLV }`。
- `type Source int`：`SourceDirect`、`SourceV1`、`SourceV2`（仅用于日志/测试，不进 wire）。
- `type Reader interface { Read(br *bufio.Reader) (hdr Header, src Source, err error) }`
  - 用 `*bufio.Reader` 以便探测时多读的字节留在缓冲、后续应用层读取可继续取用。
  - 返回约定：`err==nil` 表示成功；`src==SourceDirect` 表示无 PROXY 头（直连），此时 `hdr` 为零值；`err!=nil` 表示**畸形**头（magic 对上但解析失败或不完整）。
- `type Writer interface { WriteTo(w io.Writer, hdr Header) error }`：**始终写 v2**。
- `type AddrConn interface { LocalAddr() net.Addr; RemoteAddr() net.Addr }`：最小接口，`transport.Conn` 天然满足，避免 `proxyproto`→`transport` 的 import 环。
- `func HeaderFromConn(c AddrConn) Header`：用 `c.RemoteAddr()` 作 Src、`c.LocalAddr()` 作 Dst，按地址族映射 `FamilyTCP4/TCP6`，填入 `Header`。供直连分支构造头使用。
- adapter 在 `goproxyproto` 子包：`func NewReader() proxyproto.Reader`、`func NewWriter() proxyproto.Writer`。Gateway 只持有 `proxyproto.Reader`/`proxyproto.Writer` 接口与 policy 值，由 `main` 注入 `goproxyproto.NewReader()/NewWriter()` 实例与 policy。

`internal/transport`（传输抽象，零第三方依赖）：

- `type Conn interface { io.Reader; io.Writer; io.Closer; LocalAddr() net.Addr; RemoteAddr() net.Addr; CloseWrite() error; SetReadDeadline(time.Time) error }`。`CloseWrite` 用于半关闭（下游看到 FIN）；`SetReadDeadline` 仅在 PROXY 头探测期间使用（见下）。
- `type Listener interface { Accept() (Conn, error); Close() error; Addr() net.Addr }`。
- `type Dialer interface { Dial(network, address string) (Conn, error) }`。
- TCP 适配器：`Listen(network, addr) (Listener, error)` 包 `net.Listen`（内部 `tcpListener` 持 `*net.TCPListener`）；`*net.TCPConn` 直接满足 `Conn`，无需额外 wrapper（`tcpListener.Accept` 经 `AcceptTCP` 返回 `*net.TCPConn`）；`TCPDialer.Dial` 用 `net.Dial` 并类型断言为 `*net.TCPConn`。

### 探测逻辑与 policy（adapter 实现 Reader，Gateway 落地 policy）

`Reader.Read(br *bufio.Reader)` **直接委托 `proxyproto.Read(br)`**。go-proxyproto v0.7 的 `Read` 源码注释明确「为加速小包非 PROXY 流量，先只 Peek 1 字节」——这恰好满足「首字节即可排除 PROXY、不为直连流量引入多余阻塞」的要求，故 adapter 不重写探测、只做结果映射：

1. 库 `Read` 先 `br.Peek(1)`：只在「对端一个字节都没发」时阻塞；有 1 字节即可判定。
2. 首字节非 `'P'`(SIGV1[:1]) 且非 `'\r'`(SIGV2[:1]) → 库返回 `ErrNoProxyProtocol`（直连），**不消费字节**（Peek 不前进，后续 `io.Copy` 自然读走）。
3. 首字节命中候选 → 库按需 `Peek(5)`/`Peek(12)` 确认签名：确认则 `parseVersion1/2` 解析；前缀命中但后续 `EOF` → 库返回 `ErrNoProxyProtocol`（按直连处理、保留字节——极可能是恰好以 "PROX…" 开头的应用数据，而非被截断的真头）；签名命中但解析失败 → 库返回其它错误（畸形）。

adapter 映射：

- `ErrNoProxyProtocol` → `SourceDirect` + 零值 hdr、`err=nil`（字节保留在 br）。
- 读超时（`net.Error` 且 `Timeout()`）→ `SourceDirect` + 零值 hdr、`err=nil`（见下「探测超时」）。
- 其它 `err` → 原样返回（畸形 / IO 错）；Gateway 关闭连接。
- `*Header` → `TCPAddrs()` 取 src/dst，按 `To4()` 定族映射为 `proxyproto.Header`；`Version==2`→`SourceV2`，否则 `SourceV1`。非 TCP 族（unix/udp）超出范围 → 返回 `err`。

探测超时：库 `Read` 的 `Peek(1)` 短路解决了「首字节即可排除 PROXY」的直连零延迟，但**候选前缀不完整**（首字节 `'P'`/`'\r'`，却始终没凑够 5/12 字节）时 `Peek(5)`/`Peek(12)` 会无限阻塞——典型如直连客户端发 `'P'` 开头的短命令后等响应。Gateway 在调 `reader.Read` 前 `c.SetReadDeadline(now+detectTimeout)`、返回后立即清零（管道以普通阻塞读运行）；adapter 把该超时映射为 `SourceDirect`（Peek 不消费，已读字节留在 br、随 `io.Copy` 转发，无丢字）。这避免了 `gop.ReadTimeout` 那种「超时后被遗弃 goroutine 仍操作 br」的并发访问问题——deadline 让 `Peek` 本身返回，无遗留 goroutine。

「畸形 → 关闭、不回退直连」保留：签名命中却解析失败说明发送方声称带 PROXY 头却不可信。policy 与上版一致（Gateway 在拿到 `Source` 后落地，回答「谁允许发 PROXY Header」）：

- `policy=use`（默认，对应 S1 三种都收）：`Direct`→用 `HeaderFromConn`；`V1/V2`→用解析出的 hdr；畸形 `err`→关闭。
- `policy=require`：必须带 PROXY 头。`Direct`→关闭（拒绝）；`V1/V2`→用 hdr；畸形→关闭。
- `policy=reject`：禁止带 PROXY 头（防上游滥用伪造源）。`V1/V2`→关闭；`Direct`→用 `HeaderFromConn`；畸形→关闭。

policy 由 `main` 注入 Gateway（构造时传入），adapter 的 Reader 不感知 policy，只如实报告 `Source`。换库时新 adapter 复刻「Read→(Direct|err|*Header)」这一契约即可。

> 关于 go-proxyproto 自带 policy：其 Listener wrapper 的 Policy 字段语义与上述一致，但本设计选择「自己渐进 Peek + 仅把已确认头交库解析」，以把判定时序与 policy 归于自有抽象、可独立于库演进；库只负责它擅长的「已确认头的 wire 解析」与「v2 序列化」。换库时新 adapter 复刻同一契约即可。

### 每连接处理（Gateway）

```
accept c (transport.Conn)
br := bufio.NewReader(c)
hdr, src, err := reader.Read(br)            // 渐进 Peek 探测
switch {
case err != nil:                              log + close(c); return        // 畸形 → 关闭
case policy == reject  && src != SourceDirect: log + close(c); return       // reject + 带头 → 关闭
case policy == require && src == SourceDirect: log + close(c); return      // require + 无头 → 关闭
case src == SourceDirect: hdr = proxyproto.HeaderFromConn(c)               // use/reject + 直连 → socket 地址
}
up, err := dialer.Dial("tcp", upstream)
if err != nil: log + close(c); return
if err := writer.WriteTo(up, hdr); err != nil: log + close both; return
// 双向透传，br（可能含探测时 Peek 的应用数据）作为 client→upstream 的源
go copyAndCloseWrite(up, br)                 // client→upstream；EOF 后 up.CloseWrite()
go copyAndCloseWrite(c, up)                  // upstream→client；EOF 后 c.CloseWrite()
wait both, then close(c), close(up)
```

半关闭：一方 EOF 后对另一方调 `CloseWrite()`，使对端收到 FIN；两方向都结束后 `Close()` 全部。`io.Copy` 复制字节。

### 直连头语义

直连连接：Src = `c.RemoteAddr()` 真实客户端地址；Dst = `c.LocalAddr()` 监听套接字的本地地址；Family 按地址类型 TCP4/TCP6。**不**用 UNSPEC——本服务的目的就是保住真实客户端信息，直连时套接字地址正是真实信息。UNSPEC 仅用于「无法判定」的情况，本设计不产生该情况。

### PROXY v2 wire 格式（测试 oracle 基准）

序列化经 `goproxyproto` adapter 委托 go-proxyproto 完成，但测试用**按规范手算的字节字面量**作独立 oracle（不调库）：

- 12 字节签名：`0D 0A 0D 0A 00 0D 0A 51 55 49 54 0A`（ASCII "QUIT\n" 前缀）
- 第 13 字节：`(ver<<4)|cmd`，v2 PROXY = `21`（ver=2, cmd=1 PROXY）
- 第 14 字节：`(family<<4)|transport`，TCP4=STREAM → `11`；TCP6=STREAM → `21`；UNSPEC=STREAM → `01`
- 第 15–16 字节：地址块长度（uint16 BE）
- 地址块（TCP4）：SrcIP(4) DstIP(4) SrcPort(2) DstPort(2) = 12 字节
- 地址块（TCP6）：SrcIP(16) DstIP(16) SrcPort(2) DstPort(2) = 36 字节

示例（TCP4，Src=192.0.2.1:1234，Dst=198.51.100.1:8080）应为 28 字节：
```
0D 0A 0D 0A 00 0D 0A 51 55 49 54 0A  21  11  00 0C
C0 00 02 01  C6 33 64 01  04 D2  1F 90
```

### 错误与生命周期

- 上游拨号失败：关闭客户端连接、记一行日志、不重试、不排队。
- 转发中任一方出错：按 EOF 同等处理（CloseWrite 对端），最终关闭两侧。
- 主进程收到 SIGINT/SIGTERM：停止 Accept（`Listener.Close()`），对在跑的连接不强制中断（让 `io.Copy` 自然结束或随进程退出）。graceful 但非 drain-到-0。

## [S3] Out of Scope

- **UDP**：PROXY v2 支持 UDP，但本服务只做 TCP。
- **TLS 终止/解析**：不拆 TLS，纯字节透传，TLS 作为应用层透明穿过。
- **多下游/按目标路由**：单一固定下游，不根据 PROXY 头里的目的地址做路由。
- **下游鉴权 / 连接池 / keepalive 复用**：每上游连接新建一条下游连接。
- **配置文件 / 多监听端口 / 热重载**：仅 CLI flag。
- **PROXY v1 输出 / TLV forwarding**：只输出标准 v2 头，不搬运上游 TLV；`Header.TLVs` 字段仅为架构预留，第一版不填充不输出。
- **限流 / 速率 / ACL**：无。

## Tasks

- [x] T1: `internal/proxyproto` 协议抽象 —— `Header`/`Family`/`Source`/`TLV` 类型（`Header` 含预留 `TLVs []TLV` 字段）+ `Reader`/`Writer` 接口 + `HeaderFromConn`。验收：`go build ./internal/proxyproto/...` 通过；`HeaderFromConn` 对假 TCP4/TCP6 conn 的单元测试给出正确族与地址（covers: S2 抽象）
- [x] T2: `internal/proxyproto/goproxyproto` 适配器 —— `NewReader()/NewWriter()`；Reader 委托 `proxyproto.Read`（库自带 Peek(1) 短路直连）做探测+解析、`ErrNoProxyProtocol`→`SourceDirect` 不消费字节、其它 err→畸形；Writer 用 `proxyproto.HeaderProxyFromAddrs(2, src, dst)` + `WriteTo` 做 v2 序列化。验收：规范手算字节做 oracle 的单元测试——v1/v2 样例解析出正确 `Header` 且应用数据仍留在 br；`Writer.WriteTo` 对 TCP4/TCP6 样例输出等于上文 wire 字面量；首字节 `0x01` 的直连样例返回 `SourceDirect` 且不消费字节；签名命中但解析失败返回 `err`（covers: S2 探测逻辑与 policy 的探测部分、S2 wire；depends: T1）
- [x] T3: `internal/transport` 传输抽象 —— `Conn`/`Listener`/`Dialer` 接口 + TCP 适配器（`Listen`、`tcpConn`、`TCPDialer`）；`Conn` 含 `SetReadDeadline`。验收：`go build` 通过；用真实 `net.Listen` + 适配器 + `io.Copy` 往返字节、并验证 `CloseWrite` 把 FIN 传到对端的单元测试（covers: S2 抽象）
- [x] T4: `internal/gateway` 监听循环 + 每连接处理器（构造时注入 policy 与 detectTimeout、探测期间设/清读超时），只依赖 `proxyproto`/`transport` 接口。验收：集成测试用假下游记录器——`use` policy 下分别以直连/v1/v2 连入、发 payload、断言下游收到合法 v2 头（Src/Dst 正确）+ payload、响应能回客户端、半关闭下游见 FIN；另测 `require` 拒绝直连、`reject` 拒绝带 v2 头的连接；并测「候选前缀不完整且不发更多字节」（如直连发 `PING` 后等响应）在 detectTimeout 后被当作直连转发、不死锁（covers: S2 探测逻辑与 policy、S2 探测超时、S2 每连接处理、S2 直连头语义、S2 半关闭；depends: T1,T2,T3）
- [x] T5: `main.go` 组合根 + CLI flag（`-listen`/`-upstream`/`-policy`/`-detect-timeout`）+ 装配 adapter 与 policy/detectTimeout 注入 + 信号 graceful shutdown。验收：`go build ./...` 与 `go vet ./...` 通过；`-upstream` 缺省退出码 2；非法 `-policy` 退出码 2；所有测试通过；smoke：起 echo 下游，`proxydge -listen :9001 -upstream 127.0.0.1:<echo>`，直连/v1/v2 三种客户端均经下游收到正确 v2 头（covers: S2 配置、S2 探测逻辑与 policy、S2 错误与生命周期；depends: T4）
