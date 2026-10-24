# Monitor · 本机资源监控面板

一个零依赖部署的本机监控工具：启动一个二进制文件，用浏览器打开页面，即可实时查看 CPU、内存、磁盘、网络与进程状态。

- **三平台支持**：macOS、Linux、Windows（amd64 / arm64）
- **单文件交付**：前端资源通过 `go:embed` 打包进二进制，无需 Node、Python 或数据库
- **实时推送**：基于 SSE 每秒推送采样数据，断线自动重连；不支持 SSE 时回退轮询
- **历史趋势**：内存中保留最多 1 小时采样，页面可切换 1m / 5m / 15m / 1h 窗口
- **只读安全**：没有任何写操作，默认仅监听回环地址，可选访问令牌
- **响应式深色主题**：桌面与移动端均可浏览

## 快速开始

需要 Go 1.26+（仅从源码构建时需要，运行预编译二进制不需要任何环境）。

```bash
# 直接运行并自动打开浏览器
go run . -open

# 或先编译
make build
./monitor -open
```

默认地址：<http://127.0.0.1:9090>

## 跨平台构建

```bash
make cross
```

产物位于 `dist/`：

```
monitor-darwin-amd64      monitor-darwin-arm64
monitor-linux-amd64       monitor-linux-arm64
monitor-windows-amd64.exe monitor-windows-arm64.exe
SHA256SUMS
```

Windows 上没有 make 时：

```powershell
go build -trimpath -ldflags "-s -w" -o monitor.exe .
.\monitor.exe -open
```

国内网络下载依赖：

```bash
export GOPROXY=https://goproxy.cn,direct
go mod download
```

## 配置

命令行参数优先于环境变量。环境变量便于在 systemd、Docker 或 Windows 服务中配置。

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-listen` | `MONITOR_LISTEN` | `127.0.0.1:9090` | 监听地址，局域网访问用 `0.0.0.0:9090` |
| `-interval` | `MONITOR_INTERVAL` | `1s` | 采样间隔，范围 200ms – 1m |
| `-token` | `MONITOR_TOKEN` | 空 | 设置后 API 需要令牌 |
| `-open` | — | `false` | 启动后自动打开浏览器 |
| `-version` | — | — | 打印版本并退出 |

示例：

```bash
./monitor -listen 0.0.0.0:9090 -interval 2s -token my-secret
```

设置令牌后，页面地址需要带上 `?token=my-secret`，API 也可使用请求头：

```bash
curl -H "X-Monitor-Token: my-secret" http://127.0.0.1:9090/api/snapshot
```

## 页面内容

- **CPU**：总使用率、user / system / iowait 占比、各逻辑核心使用率
- **内存**：使用率、可用 / 缓存 / Swap
- **网络**：实时上下行速率、累计流量、活动网卡
- **磁盘 IO**：读写速率与 IOPS
- **趋势图**：CPU 与内存、网络吞吐，支持鼠标悬停查看具体数值
- **磁盘使用**：各挂载点容量与使用率
- **进程排行**：按 CPU 或内存排序的 Top 12 进程
- **主机信息**：系统版本、内核、CPU 型号、核心数、开机时间、采样间隔

## HTTP API

| 路径 | 说明 |
| --- | --- |
| `GET /api/health` | 健康检查（无需令牌） |
| `GET /api/meta` | 版本、采样间隔、历史窗口、服务运行时长 |
| `GET /api/snapshot` | 当前完整快照（JSON） |
| `GET /api/history?seconds=3600` | 历史采样，列式数组，最长 86400 秒 |
| `GET /api/stream` | SSE 实时推送，事件名 `tick`，每 20 秒心跳 |

```bash
curl -s http://127.0.0.1:9090/api/snapshot | jq
curl -N http://127.0.0.1:9090/api/stream
```

## 后台常驻

### Linux（systemd）

```ini
# /etc/systemd/system/monitor.service
[Unit]
Description=Monitor local resource dashboard
After=network.target

[Service]
ExecStart=/usr/local/bin/monitor -listen 127.0.0.1:9090
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now monitor
```

### macOS（launchd）

```xml
<!-- ~/Library/LaunchAgents/local.monitor.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>local.monitor</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/monitor</string>
    <string>-listen</string>
    <string>127.0.0.1:9090</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/local.monitor.plist
```

### Windows

直接运行 `monitor-windows-amd64.exe -open`，或用任务计划程序配置"计算机启动时"运行，也可借助 [NSSM](https://nssm.cc/) 注册为服务：

```powershell
nssm install Monitor "C:\tools\monitor.exe" "-listen 127.0.0.1:9090"
nssm start Monitor
```

## 项目结构

```
.
├── main.go                     # 启动、配置、优雅退出
├── internal/
│   ├── collector/              # 指标采集（gopsutil）
│   │   ├── collector.go        # 采样循环、速率计算
│   │   ├── history.go          # 内存历史环形缓冲
│   │   ├── hub.go              # SSE 订阅广播
│   │   ├── system.go           # CPU / 内存 / 负载 / 主机信息
│   │   ├── disk.go             # 分区与磁盘 IO
│   │   ├── network.go          # 网卡吞吐
│   │   └── process.go          # 进程排行（独立采样）
│   └── server/                 # HTTP API、SSE、鉴权、静态资源
├── web/
│   ├── embed.go                # go:embed 入口
│   └── static/                 # 原生 HTML / CSS / JS
├── Makefile                    # 构建、测试、交叉编译
└── go.mod
```

## 设计说明

- **为什么用 Go**：单二进制、`GOOS/GOARCH` 交叉编译、标准库自带 HTTP 与嵌入资源，最适合"每台机器放一个"的部署方式。
- **采集层**：`gopsutil` 抹平 Linux `/proc`、macOS `sysctl`、Windows `PDH` 的差异。
- **速率计算**：网卡与磁盘使用累计计数器做差，重启或计数器回绕时自动归零，避免负数。
- **历史数据**：环形缓冲区保留最多 1 小时采样（至少 300 个点，最多 20000 个点），页面初次加载即可看到趋势。
- **进程采样**：进程枚举较重，独立协程按 2 秒（或采样间隔）刷新，不阻塞主采样节奏。
- **前端**：原生 JS + Canvas 自绘图表，无 CDN、无构建步骤，离线可用。

## 已知限制

- Windows 不提供 load average，页面会隐藏负载指标。
- 进程 CPU 百分比相对于**单核**，与 `ps`、任务管理器口径一致；多核满载时最高可达 `100 × 核数`。
- 在容器中运行时，读取到的是容器可见的 CPU/内存限制与 `/proc` 数据，不是宿主机全貌。
- macOS 只展示根分区与 `/Volumes/*` 下的卷；磁盘 IO 为整盘聚合值。
- 历史数据仅存于内存，重启后清空。
- 默认监听 `127.0.0.1`。若需要远程访问，请设置 `-token` 并通过防火墙或反向代理限制来源；页面本身会暴露主机名、进程名等信息。

## 开发

```bash
make test      # 单元与接口测试
make race      # 数据竞争检测
make vet       # 静态检查
make cross     # 六平台交叉编译
```
