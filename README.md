# Monitor · 本机资源监控面板

一个零依赖部署的本机监控工具：启动一个二进制文件，用浏览器打开页面，即可实时查看 CPU、内存、GPU、磁盘、网络与进程状态。

- **三平台支持**：macOS、Linux、Windows（amd64 / arm64）
- **单文件交付**：前端资源通过 `go:embed` 打包进二进制，内置 SQLite，无需 Node、Python 或额外数据库服务
- **实时推送**：基于 SSE 每秒推送采样数据，断线自动重连；不支持 SSE 时回退轮询
- **持久化历史**：SQLite 存储 1 秒全明细 14 天、1 分钟聚合 90 天、1 小时聚合 2 年，重启不丢失
- **历史明细**：页面支持 1m / 5m / 15m / 1h / 1d / 7d / 30d / 1y 范围，点击曲线可查看该时刻的分核、GPU、磁盘与进程明细
- **GPU 监控**：自动检测显卡，展示利用率、显存、温度、功耗、风扇与趋势曲线
- **只读安全**：没有任何写操作，默认仅监听回环地址，可选访问令牌
- **响应式布局**：桌面与移动端均可浏览
- **明暗双主题**：浅色 / 深色一键切换，首次访问跟随系统，选择会被记住

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
| `-db` | `MONITOR_DB` | 系统数据目录 | SQLite 路径；设为 `off` 关闭持久化 |
| `-raw-retention` | `MONITOR_RAW_RETENTION` | `336h`（14 天） | 1 秒原始数据保留时长 |
| `-minute-retention` | `MONITOR_MINUTE_RETENTION` | `2160h`（90 天） | 1 分钟聚合保留时长 |
| `-hour-retention` | `MONITOR_HOUR_RETENTION` | `17520h`（730 天） | 1 小时聚合保留时长 |
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

## 历史数据与存储

默认启用本地 SQLite，首次启动会自动创建数据库，无需安装任何数据库服务。使用纯 Go 驱动，仍然保持 `CGO_ENABLED=0` 的六平台交叉编译。

默认位置：

| 平台 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/monitor/monitor.db` |
| Linux | `$XDG_DATA_HOME/monitor/monitor.db`，否则 `~/.local/share/monitor/monitor.db` |
| Windows | `%LOCALAPPDATA%\monitor\monitor.db` |

如果默认目录不可写，程序会自动退回内存模式并打印警告；也可以用 `-db` 指定一个可写路径。

保留策略：

| 数据 | 精度 | 保留 | 用途 |
| --- | --- | --- | --- |
| 总体指标 + 分核/GPU 明细 + 进程排行 | 1 秒 | 14 天 | 精确回看、历史明细 |
| 总体指标 + 分核/GPU/磁盘/进程聚合 | 1 分钟 | 90 天 | 日/周趋势、聚合明细 |
| 总体指标 + 分核/GPU/磁盘/进程聚合 | 1 小时 | 2 年 | 长期容量规划 |
| 磁盘容量 | 60 秒 | 90 天 | 慢变化指标，避免重复写入 |

数据量取决于核心数、磁盘数、网卡数和 Top 进程数，典型单机 14 天全明细约 **0.5–1.5 GB**，长期聚合数据通常只有几十到几百 MB。

实现方式：

- 采样协程通过有界队列交给独立写入协程，每 2 秒或 8 条批量提交，不阻塞实时 SSE；
- WAL 模式、`synchronous=NORMAL`，数据库单连接串行访问，避免 SQLite 锁竞争；
- 进程名使用字典表，避免每秒重复存储字符串；
- 1 分钟 / 1 小时聚合由后台任务生成，重启后自动补齐最近窗口；
- 数据库不可用时自动退回纯内存模式，页面实时监控不受影响；
- 数据库文件权限 `0600`，只允许单进程访问，**不要放在 NFS / SMB 等网络文件系统上**。

关闭持久化（仅保留最近约 1 小时内存历史）：

```bash
./monitor -db off
```

自定义路径与保留时长：

```bash
./monitor -db /var/lib/monitor/monitor.db -raw-retention 336h -minute-retention 2160h
```

## 页面内容

- **CPU**：总使用率、user / system / iowait 占比、各逻辑核心使用率
- **内存**：使用率、可用 / 缓存 / Swap
- **网络**：实时上下行速率、累计流量、活动网卡
- **磁盘 IO**：读写速率与 IOPS
- **GPU**：利用率、显存、温度、功耗、风扇与利用率趋势（仅在检测到 GPU 时显示）
- **趋势图**：CPU 与内存、网络吞吐、GPU 利用率，支持 1m / 5m / 15m / 1h / 1d / 7d / 30d / 1y 切换
- **历史明细**：点击利用率曲线，可查看该时刻的 CPU 分核、内存、GPU、各磁盘容量和进程排行
- **磁盘使用**：各挂载点容量与使用率
- **进程排行**：按 CPU 或内存排序的 Top 12 进程
- **主机信息**：系统版本、内核、CPU 型号、核心数、开机时间、采样间隔

## GPU 支持

页面会自动探测 GPU，未检测到设备时整个 GPU 区块隐藏。

| 平台 | 数据来源 | 可用指标 |
| --- | --- | --- |
| macOS（Apple Silicon / AMD / Intel） | `ioreg` 的 `IOAccelerator` 统计 | 利用率、显存占用、核心数 |
| Linux + NVIDIA | `nvidia-smi` | 利用率、显存、温度、功耗、风扇 |
| Linux + AMD | `/sys/class/drm` 与 hwmon | 利用率、显存、温度、功耗 |
| Linux + Intel / 其他 DRM | `/sys/class/drm` | 设备名称 |
| Windows + NVIDIA | `nvidia-smi` | 利用率、显存、温度、功耗、风扇 |
| Windows + AMD / Intel | WMI `Win32_VideoController` | 设备名称 |

GPU 数据独立每 3 秒刷新一次，避免驱动查询拖慢主采样。

## 主题切换

页面右上角提供太阳 / 月亮按钮，可在浅色与深色主题之间切换：

- 首次访问遵循系统的浅色 / 深色外观（`prefers-color-scheme`）
- 手动切换后写入 `localStorage` 的 `monitor.theme`，刷新或重新打开浏览器后保持不变
- 切换会同步重绘趋势图配色，无需刷新页面
- 未手动选择时，系统外观变化会自动跟随


## HTTP API

| 路径 | 说明 |
| --- | --- |
| `GET /api/health` | 健康检查（无需令牌） |
| `GET /api/meta` | 版本、采样间隔、历史窗口、服务运行时长、存储状态 |
| `GET /api/snapshot` | 当前完整快照（JSON） |
| `GET /api/history?seconds=86400` | 历史采样，列式数组，自动选择 1s / 1m / 1h 分辨率 |
| `GET /api/history/details?ts=1730000000000` | 指定毫秒时间点的历史明细（分核、GPU、磁盘、进程） |
| `GET /api/stream` | SSE 实时推送，事件名 `tick`，每 20 秒心跳 |

`/api/snapshot` 中的 `gpus` 字段在未检测到 GPU 时省略；`/api/history` 的 `gpu` 列取所有设备中的最高利用率，响应中的 `resolution` 与 `step` 分别表示数据源分辨率和实际显示步长。

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
│   │   ├── gpu.go              # GPU 通用解析与异步采样
│   │   ├── gpu_darwin.go       # macOS IOAccelerator
│   │   ├── gpu_linux.go        # nvidia-smi / DRM sysfs
│   │   ├── gpu_windows.go      # nvidia-smi / WMI
│   │   └── process.go          # 进程排行（独立采样）
│   ├── historydb/              # SQLite 历史存储
│   │   ├── db.go               # 建表、生命周期、异步队列
│   │   ├── writer.go           # 批量写入与进程字典
│   │   ├── query.go            # 范围查询、降采样、明细
│   │   ├── rollup.go           # 1m / 1h 聚合
│   │   └── prune.go            # 保留策略与清理
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
- **历史数据**：内存环形缓冲作为实时回退，SQLite 作为持久化来源；1 秒原始明细保留 14 天，1 分钟聚合 90 天，1 小时聚合 2 年。
- **SQLite 写入**：采样与落盘解耦，后台协程批量事务写入；WAL + 单连接避免锁竞争；进程名去重存储；后台任务生成聚合与执行保留清理。
- **进程采样**：进程枚举较重，独立协程按 2 秒（或采样间隔）刷新，不阻塞主采样节奏。
- **GPU 采样**：macOS 解析 `IOAccelerator` 性能统计，Linux / Windows 优先调用 `nvidia-smi`，AMD Linux 回退 sysfs；独立协程每 3 秒刷新。
- **前端**：原生 JS + Canvas 自绘图表，无 CDN、无构建步骤，离线可用。

## 已知限制

- Windows 不提供 load average，页面会隐藏负载指标。
- Windows 非 NVIDIA 显卡、Linux Intel 显卡只能读取设备名称，无法获取利用率。
- Apple Silicon 的 GPU 使用统一内存，只能显示占用量而没有显存总量；GPU 温度与功耗需要 root 权限读取 `powermetrics`，默认不采集。
- GPU 趋势曲线取所有设备中的最高利用率，采样间隔为 3 秒。
- 进程 CPU 百分比相对于**单核**，与 `ps`、任务管理器口径一致；多核满载时最高可达 `100 × 核数`。
- 在容器中运行时，读取到的是容器可见的 CPU/内存限制与 `/proc` 数据，不是宿主机全貌。
- macOS 只展示根分区与 `/Volumes/*` 下的卷；磁盘 IO 为整盘聚合值。
- 1 秒明细仅保留 14 天；更早的数据来自 1 分钟或 1 小时聚合，历史明细会标记为近似值。
- 磁盘容量按 60 秒采样，历史明细中的磁盘使用率可能与该秒的实际值有最多 1 分钟偏差。
- 历史数据库只支持单进程访问，且不能放在网络文件系统上；数据库不可用时会自动退回内存模式（约 1 小时）。
- 默认监听 `127.0.0.1`。若需要远程访问，请设置 `-token` 并通过防火墙或反向代理限制来源；页面本身会暴露主机名、进程名等信息。

## 开发

```bash
make test      # 单元与接口测试
make race      # 数据竞争检测
make vet       # 静态检查
make cross     # 六平台交叉编译
```
