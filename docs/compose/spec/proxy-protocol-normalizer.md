---
feature: proxy-protocol-normalizer
status: designed
updated: 2026-08-20
branch: feat/proxy-protocol-normalizer
commits:
---

# Proxy Protocol Normalizer (ProxyDge)

## Report

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
- 仅 TCP。日志打到 stderr，每连接一行：accept（含检测到的来源类型）、upstream 拨号失败/成功、转发结束原因。

### 架构：库隔离抽象（用户硬性要求）

业务代码**不得直接 import `github.com/pires/go-proxyproto`**。库被挡在自有的抽象后面，以便将来替换库时只改 adapter 子包、不碰 Gateway。

包布局（module `proxydge`）：

```
main.go                                  # 组合根：解析 flag、装配 adapter、跑 Gateway、信号处理
internal/proxyproto/proxyproto.go        # 自有 Header 类型 + Reader/Writer 接口 + HeaderFromConn
internal/proxyproto/goproxyproto/        # 适配器：用 go-proxyproto 实现 proxyproto.Reader/Writer
internal/transport/transport.go          # 自有 Conn/Listener/Dialer 接口 + TCP 适配器
internal/gateway/gateway.go             # 监听循环 + 每连接处理器，只依赖上述接口
internal/gateway/gateway_test.go        # 集成测试（真实 proxyproto adapter + 假下游记录器 + 假 transport）
```

`internal/proxyproto`（协议抽象，零第三方依赖）：

- `type Family int`：`FamilyUnspec`、`FamilyTCP4`、`FamilyTCP6`。
- `type Header struct { SrcIP, DstIP net.IP; SrcPort, DstPort uint16; Family Family }`。
- `type Source int`：`SourceDirect`、`SourceV1`、`SourceV2`（仅用于日志/测试，不进 wire）。
- `type Reader interface { Read(br *bufio.Reader) (hdr Header, src Source, err error) }`
  - 用 `*bufio.Reader` 以便探测时多读的字节留在缓冲、后续应用层读取可继续取用。
  - 返回约定：`err==nil` 表示成功；`src==SourceDirect` 表示无 PROXY 头（直连），此时 `hdr` 为零值；`err!=nil` 表示**畸形**头（magic 对上但解析失败）。
- `type Writer interface { WriteTo(w io.Writer, hdr Header) error }`：**始终写 v2**。
- `func HeaderFromConn(c transport.Conn) Header`：用 `c.RemoteAddr()` 作 Src、`c.LocalAddr()` 作 Dst，按地址族映射 `FamilyTCP4/TCP6`，填入 `Header`。供直连分支构造头使用。
- adapter 在 `goproxyproto` 子包：`func NewReader() proxyproto.Reader`、`func NewWriter() proxyproto.Writer`。Gateway 只持有 `proxyproto.Reader`/`proxyproto.Writer` 接口，由 `main` 注入 `goproxyproto.NewReader()/NewWriter()` 实例。

`internal/transport`（传输抽象，零第三方依赖）：

- `type Conn interface { io.Reader; io.Writer; io.Closer; LocalAddr() net.Addr; RemoteAddr() net.Addr; CloseWrite() error }`。`CloseWrite` 用于半关闭（下游看到 FIN）。
- `type Listener interface { Accept() (Conn, error); Close() error; Addr() net.Addr }`。
- `type Dialer interface { Dial(network, address string) (Conn, error) }`。
- TCP 适配器：`Listen(network, addr) (Listener, error)` 包 `net.Listen`；`tcpConn` 包 `*net.TCPConn`；`Dialer` 用 `net.Dial`。

### 探测逻辑（adapter 实现，契约在 proxyproto 层）

对每个上游连接，用 `bufio.NewReader(c)` 包装后调 `Reader.Read(br)`。adapter 内部探测方式（实现细节，但行为契约固定）：

1. `br.Peek(6)`。
2. 若前 6 字节 == `"PROXY "`（`50 52 4F 58 59 20`）→ v1：交给 go-proxyproto 解析文本头 → 映射为 `Header`，`src=SourceV1`。畸形 → 返回 `err`。
3. 若前 6 字节 == v2 签名前缀 `\r\n\r\n\x00\r\n`（`0D 0A 0D 0A 00 0D 0A` 的前 6 = `0D 0A 0D 0A 00 0D`）→ 确认 v2 签名（再 Peek(12) 比对完整 12 字节签名 `0D0A0D0A000D0A5154585450`），交给 go-proxyproto 解析二进制头 → 映射为 `Header`，`src=SourceV2`。畸形 → 返回 `err`。
4. 否则 → 无 PROXY 头（直连）：返回 `hdr` 零值、`src=SourceDirect`、`err=nil`；**不消费任何字节**（`br` 保留 Peek 的数据，应用层随后的 `io.Copy` 自然读走）。

畸形（magic 对上但 go-proxyproto 解析失败）→ 返回 `err`；Gateway 关闭该连接并记日志，**不**回退为直连（发送方声称带了头却解析失败，不能信任其字节）。

> 说明：选择「自己 Peek magic + 仅把已确认的头交给 go-proxyproto 解析」而非「直接用库的 optional-header 读取」，是为了让「有/无头」的判定由我们的简单 magic 比较掌控、行为确定；库只负责它擅长的「已确认头的 wire 解析」与「v2 序列化」。换库时，新 adapter 只需复刻同一契约。

### 每连接处理（Gateway）

```
accept c (transport.Conn)
br := bufio.NewReader(c)
hdr, src, err := reader.Read(br)            // 直连返回 SourceDirect + 零值 hdr
if err != nil: log + close(c); return
if src == SourceDirect:
    hdr = proxyproto.HeaderFromConn(c)      // 真实 peer + 真实 local 地址
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

- 12 字节签名：`0D 0A 0D 0A 00 0D 0A 51 54 58 54 50`
- 第 13 字节：`(ver<<4)|cmd`，v2 PROXY = `21`（ver=2, cmd=1 PROXY）
- 第 14 字节：`(family<<4)|transport`，TCP4=STREAM → `11`；TCP6=STREAM → `21`；UNSPEC=STREAM → `01`
- 第 15–16 字节：地址块长度（uint16 BE）
- 地址块（TCP4）：SrcIP(4) DstIP(4) SrcPort(2) DstPort(2) = 12 字节
- 地址块（TCP6）：SrcIP(16) DstIP(16) SrcPort(2) DstPort(2) = 36 字节

示例（TCP4，Src=192.0.2.1:1234，Dst=198.51.100.1:8080）应为 28 字节：
```
0D 0A 0D 0A 00 0D 0A 51 54 58 54 50  21  11  00 0C
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
- **PROXY v1 输出 / TLV 透传自定义**：只输出标准 v2 头，不搬运上游 TLV。
- **限流 / 速率 / ACL**：无。

## Tasks

- [ ] T1: `internal/proxyproto` 协议抽象 —— `Header`/`Family`/`Source` 类型 + `Reader`/`Writer` 接口 + `HeaderFromConn`。验收：`go build ./internal/proxyproto/...` 通过；`HeaderFromConn` 对假 TCP4/TCP6 conn 的单元测试给出正确族与地址（covers: S2 抽象）
- [ ] T2: `internal/proxyproto/goproxyproto` 适配器 —— `NewReader()/NewWriter()`，用 go-proxyproto 实现已确认头的解析与 v2 序列化；Reader 内部 Peek magic 判定 v1/v2/直连/畸形。验收：用规范手算字节做 oracle 的单元测试——v1 样例与 v2 样例解析出正确 `Header`；`Writer.WriteTo` 对 TCP4/TCP6 样例输出等于上文 wire 格式字面量；直连样例返回 `SourceDirect` 且不消费字节；畸形样例返回 `err`（covers: S2 探测、S2 wire；depends: T1）
- [ ] T3: `internal/transport` 传输抽象 —— `Conn`/`Listener`/`Dialer` 接口 + TCP 适配器（`Listen`、`tcpConn`、Dialer 用 `net.Dial`）。验收：`go build` 通过；用真实 `net.Listen` + 适配器 + `io.Copy` 往返字节、并验证 `CloseWrite` 把 FIN 传到对端的单元测试（covers: S2 抽象）
- [ ] T4: `internal/gateway` 监听循环 + 每连接处理器，只依赖 `proxyproto`/`transport` 接口。验收：集成测试用假下游记录器，分别以直连/v1/v2 三种方式连入、发一段 payload、断言下游收到合法 v2 头（Src/Dst 正确）+ payload；并断言下游回写的响应能回到客户端；半关闭时下游看到 FIN（covers: S2 每连接处理、S2 直连头语义、S2 半关闭；depends: T1,T2,T3）
- [ ] T5: `main.go` 组合根 + CLI flag（`-listen`/`-upstream`）+ 装配 adapter + 信号 graceful shutdown。验收：`go build ./...` 与 `go vet ./...` 通过；`-upstream` 缺省时退出码 2；所有测试通过；smoke：起一个 echo 下游，`proxydge -listen :9001 -upstream 127.0.0.1:<echo>`，直连/v1/v2 三种客户端均能经下游收到正确 v2 头（covers: S2 配置、S2 错误与生命周期；depends: T4）
