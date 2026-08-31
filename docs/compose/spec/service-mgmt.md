---
feature: service-mgmt
status: delivered
updated: 2026-08-31
branch: feature/service-mgmt
commits: a185476..aa38eeb
---

# 跨平台服务管理 (kardianos/service)

## Report

**What was built** — 为 ProxyDge 添加了跨平台系统服务管理能力，使用 `github.com/kardianos/service v1.3.0` 支持 Windows Service、Linux systemd、macOS launchd。新增 `proxydge service install/uninstall/start/stop/status` 嵌套子命令，现有 `proxydge start` 行为完全不变。

从 `cmdStart` 中提取了 `runGateway(cfg *config.Config)` 函数，返回 `(closer, <-chan error, err)`，供 `cmdStart` 和 `proxydgeService.Start` 共用。`proxydgeService` 实现 `service.Interface`，采用单消费者 errc 模型：monitor goroutine 唯一读取 errc，fatal error 通过 `os.Exit(1)` 直接终止进程以触发 OS Recovery Action（Windows OnFailure=restart）。install 时将配置路径转为绝对路径、校验文件存在性，已安装时提示不重启。

**Verification** — `go vet ./...` PASS，`go test ./... -count=1` 全部通过（11 个包），`go build` 成功。手动验证：`proxydge service`（无参数）→ exit 2，`proxydge service status` → "not installed"，`proxydge service install` → Windows "Access is denied"（需管理员），`proxydge start -listen bad-addr` → 正常报错。

**Journey log**
- 用户反馈将 install 行为从"先卸载再安装"改为 ddns-go 模式（已安装则提示退出，不重启不覆盖）
- errc 从双消费者（Start goroutine + Stop）改为单消费者（仅 monitor goroutine），Stop 通过 done channel 等待
- fatal error 机制从 service.Run() 返回 error 改为 os.Exit(1)，确保 Windows Recovery Action 触发
- runGateway 签名从接收 config path 改为接收已加载的 *config.Config，配置加载职责在调用方

## [S1] Problem

ProxyDge 当前只能以前台进程方式运行（`proxydge start`），无法作为系统服务（Windows Service / Linux systemd / macOS launchd）自动启动和管理。生产环境需要开机自启、崩溃恢复、统一的服务生命周期管理能力。

## [S2] Design

### 2.1 依赖

引入 `github.com/kardianos/service v1.3.0`，该库提供跨平台的服务管理 API，支持 Windows XP+、Linux (systemd/Upstart/SysV/OpenRC)、macOS (launchd)。

### 2.2 CLI 结构

新增嵌套子命令 `proxydge service <action>`，与现有 `proxydge start` 并行，不修改其行为。

```
proxydge service install   [-config <path>] [-lang <locale>]   # 安装系统服务
proxydge service uninstall [-lang <locale>]                     # 卸载系统服务
proxydge service start     [-lang <locale>]                     # 启动已安装的服务
proxydge service stop      [-lang <locale>]                     # 停止已安装的服务
proxydge service status    [-lang <locale>]                     # 查询服务状态
```

`proxydge help` 输出中新增 `service` 命令说明。

### 2.3 服务配置

| 字段 | 值 |
|------|-----|
| Name | `ProxyDge` |
| DisplayName | `ProxyDge Gateway` |
| Description | `PROXY Protocol normalizing gateway for TCP and UDP` |
| Arguments | `["start", "-config", "<absolute-config-path>"]` — 复用现有 `cmdStart` 入口 |

**install 时的配置路径处理：**
- 通过 `-config` 指定配置文件路径；未指定时使用 `config.DefaultConfigPath()`
- **必须转为绝对路径**：服务进程的工作目录与交互终端不同，相对路径无法正确解析。`filepath.Abs()` 转换后再写入 Arguments
- **安装前校验文件存在性**：`os.Stat()` 检查配置文件是否存在，不存在则报错 exit 2

**install 时的幂等行为（参照 ddns-go 模式）：**
1. 调用 `s.Status()` 查询当前状态
2. 若 `status == StatusUnknown`（未安装）→ 执行 `s.Install()` + `s.Start()`，打印成功消息
3. 若 `status != StatusUnknown`（已安装，无论 Running 或 Stopped）→ 打印 "服务已安装，无需再次安装"，直接返回，**不重启、不覆盖、不报错**

**Windows 平台额外配置：** `DelayedAutoStart: true`，`OnFailure: "restart"`

### 2.4 服务生命周期模型

遵循 `kardianos/service` 的推荐模型，职责分离清晰：

```
main()
 └─ service.New(program, config)
     └─ service.Run()
          ├─ program.Start()   // 异步启动 Gateway，不阻塞
          ├─ ...Gateway 运行中...
          └─ program.Stop()    // 收到停止信号，清理并返回
```

**`proxydgeService` 结构体：**

```go
type proxydgeService struct {
    cfg    *config.Config  // 已加载的配置
    closer func()
    errc   <-chan error    // 单消费者：仅 Stop() 读取
}
```

**`Start(s service.Service) error`：**
- 调用 `runGateway(cfg)` 获取 `(closer, errc, err)`
- **不阻塞**，立即返回 nil
- **不启动任何额外 goroutine**——errc 的唯一消费者是 Stop()

**`Stop(s service.Service) error`：**
- 调用 `closer()` 关闭 listener
- 从 `errc` 读取（10s 超时保护）
- 返回 errc 中的 error（nil = 正常关闭）

**Fatal error 机制——进程直接死亡，触发 OS Recovery：**

Gateway fatal error 的传播路径故意绕过 `service.Run()`，让进程以非零退出码终止，确保 Windows OnFailure=restart / systemd Restart=always 等平台 recovery 机制生效：

```
Gateway Serve() 遇到 fatal error
      ↓
写入 errc（buffered chan，不阻塞）
      ↓
os.Exit(1)   ← 进程直接死亡
      ↓
OS Service Manager 检测到非正常退出
      ↓
执行 Recovery Action（Windows: restart / systemd: restart）
```

**必须验证的行为：** 在 T8 集成测试中，验证 fatal error 导致进程退出后，Windows Service Manager 的 Recovery Action 确实触发了重启。

**正常关闭路径（收到 SIGTERM / Stop 调用）：**
```
OS Service Manager 发送停止信号
      ↓
service.Stop() 被调用
      ↓
closer() 关闭 listener
      ↓
Serve() 正常返回 nil
      ↓
errc 收到 nil
      ↓
Stop() 读取 errc，返回 nil
      ↓
service.Run() 正常退出 → main() 返回 0
```

### 2.5 runGateway() 提取

将 `cmdStart` 中的网关创建和运行逻辑提取为可复用函数：

```go
func runGateway(cfg *config.Config) (closer func(), errc <-chan error, err error)
```

- **入参为已加载的 `*config.Config`**，配置加载由调用方（`cmdStart` / `proxydgeService.Start`）负责
- 构建 logger、创建 trust checker、根据 protocol 分支创建 TCP/UDP gateway
- 启动 goroutine 运行 `g.Serve()`，写入内部 `chan error`（容量 1），返回只读端 `<-chan error`
- 返回的 `closer` 负责关闭 listener/gateway
- **不处理信号**（信号处理由 `cmdStart` 和 `proxydgeService.Stop` 各自负责）
- **不处理 fatal error 退出**（fatal → 写入 errc → `os.Exit(1)` 由调用方决定，service 模式下由 `proxydgeService` 内部处理）

### 2.6 ServiceController 接口（可测试性）

为避免测试中硬 mock `kardianos/service` 全局函数，定义薄接口层：

```go
type ServiceController interface {
    Install() error
    Uninstall() error
    Start() error
    Stop() error
    Status() (service.Status, error)
}
```

- 生产实现：`kardianosServiceController`（内部调用 `service.Service` 的对应方法）
- 测试实现：`fakeServiceController`（可控的返回值和调用记录）
- `cmdService()` 依赖 `ServiceController` 接口，不直接依赖 `kardianos/service`

### 2.7 状态查询输出

`proxydge service status` 直接映射 `kardianos/service` 的 `Status` 常量：

| kardianos Status | 输出 |
|-----------------|------|
| `StatusRunning` | Running |
| `StatusStopped` | Stopped |
| `StatusUnknown` | Unknown + 提示未安装 |

使用 i18n 翻译状态标签。

### 2.8 错误处理

- `install` 时服务已存在 → 打印 "已安装，无需再次安装"，exit 0
- `install` 时配置文件不存在 → 打印错误，exit 2
- `uninstall/start/stop` 时服务不存在 → 打印错误，exit 2
- `status` 时服务不存在 → 打印 Unknown + 提示未安装
- 权限不足（Windows 需管理员、Linux 需 root） → 依赖 `kardianos/service` 返回的错误，原样输出

### 2.9 国际化

新增 i18n 键（en / zh-CN / zh-TW 三语言）：

| 键 | 用途 |
|----|------|
| `help.service_text` | `service` 子命令的帮助文本 |
| `service.installed` | 安装成功 |
| `service.uninstalled` | 卸载成功 |
| `service.started` | 服务已启动 |
| `service.stopped` | 服务已停止 |
| `service.status.running` | 状态：运行中 |
| `service.status.stopped` | 状态：已停止 |
| `service.status.unknown` | 状态：未知 |
| `service.already_installed` | 服务已存在，无需再次安装 |
| `service.not_installed` | 服务未安装 |
| `error.service_action` | 服务操作失败（%s=action, %v=error） |
| `error.config_not_found` | 配置文件不存在（%s=path） |

### 2.10 日志

职责分离：
- **service logger**（`service.Logger`）= 服务生命周期/框架级错误（install/uninstall 失败、Start/Stop 异常）
- **ProxyDge logger**（`*slog.Logger`）= 应用日志（connection、session、header parse 等）

两者不互相复制。Windows Event Log 只记录服务框架消息，不会被 ProxyDge 的每条连接日志淹没。

`proxydgeService.Start` 中构建的 application logger 与 `cmdStart` 一致（复用 `buildLogger`）。

## [S3] Out of Scope

- 不修改现有 `proxydge start` 的行为
- 不支持 systemd template units（`proxydge@.service`）
- 不实现 `service restart` 子命令（用户可执行 `stop` + `start`）
- 不支持 Windows 服务的自定义账户（UserName）配置
- 不支持 `proxydge service run`（前台模式直接使用 `proxydge start`）

## Tasks

- [x] T1: 提取网关运行逻辑为 `runGateway(cfg *config.Config)` 函数 — acceptance: `cmdStart` 调用 `runGateway()` 后行为不变，所有现有测试通过，`errc` 类型为 `<-chan error`，配置加载在调用方完成 (covers: S2.5)
- [ ] T2: 实现 `proxydgeService` 结构体和 `service.Interface` — acceptance: 单元测试验证 `Start()` 非阻塞启动 Gateway，`Stop()` 能关闭 Gateway 并正常返回；fatal error 导致 `os.Exit(1)` 而非通过 `service.Run()` 返回；errc 单消费者（仅 Stop 读取）无并发竞争 (covers: S2.4; depends: T1)
- [ ] T3: 定义 `ServiceController` 接口 + `kardianosServiceController` 生产实现 + `cmdService()` 分发函数 — acceptance: `proxydge service install/uninstall/start/stop/status` 各子命令正确分发，未知子命令返回错误 (covers: S2.2, S2.6)
- [ ] T4: 实现 install/uninstall 逻辑（使用 `fakeServiceController`）— acceptance: 单元测试验证 install 传入正确的 Config（Name/Arguments/DisplayName 含绝对路径），已安装时打印提示并 exit 0 不重启；配置文件不存在时报错 exit 2 (covers: S2.3, S2.8)
- [ ] T5: 实现 start/stop/status 逻辑（使用 `fakeServiceController`）— acceptance: 各操作正确调用 `ServiceController` 对应方法，输出正确的 i18n 翻译结果 (covers: S2.7)
- [ ] T6: 添加 i18n 键（en / zh-CN / zh-TW）— acceptance: 三语言文件中新增的键集合一致，`i18n_test.go` 中的键集校验通过 (covers: S2.9)
- [ ] T7: 更新 help 文本和 README — acceptance: `proxydge help` 输出包含 `service` 命令说明，README CLI Commands 章节更新 (covers: S2.2)
- [ ] T8: 集成测试 — acceptance: `go test ./...` 全部通过；手动验证 `proxydge service install` 在 Windows 上创建服务；验证 fatal error 导致进程非零退出后 Windows Service Manager Recovery Action 触发重启 (covers: S2.2, S2.3, S2.4, S2.5, S2.6, S2.8)
