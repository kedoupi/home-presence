# Home Presence

家庭局域网设备感知系统 — 实时监控多个家庭的设备在线状态。

## 功能特性

- **多家庭支持** — 服务端/客户端架构，每个家庭部署一个扫描节点
- **精准设备识别** — 多信号融合检测：OUI 厂商库（38,899 厂商）+ mDNS 服务发现 + DNS PTR + Hostname 启发式
- **设备分类** — 自动识别手机/电脑/电视/音箱/打印机等 12 种设备类别，支持手动覆盖
- **NetBIOS 名称发现** — 并发获取局域网设备的 NetBIOS 名称
- **Web 管理界面** — 支持 Dark Mode 的响应式 UI，设备图标 + 厂商/类别标签，可编辑设备信息
- **单二进制文件** — Go 编译，OUI 数据库和 Web UI 内嵌，无运行时依赖

## 安装

### 方式 1：从源码构建（需要 Go 环境）

**前置条件：** Go 1.24+

```bash
git clone https://github.com/kedoupi/home-presence.git
cd home-presence
go build -o home-presence .
```

**运行（需要 root 权限用于网络扫描）：**

```bash
sudo ./home-presence -role both -home 家庭A -port 8080
```

打开 http://localhost:8080 查看设备状态。

**可选系统依赖（增强扫描能力）：**

| 依赖 | 用途 | 安装方式 |
|------|------|---------|
| `nmblookup` | NetBIOS 名称发现 | macOS: `brew install samba` / Ubuntu: `sudo apt install samba-client` |

> 不安装 nmblookup 不影响核心功能，仅跳过 NetBIOS 名称查询。

### 方式 2：下载预编译二进制

从 [Releases](https://github.com/kedoupi/home-presence/releases) 下载对应平台的二进制：

| 平台 | 文件 |
|------|------|
| Linux x86_64（服务器/NAS） | `home-presence-linux-amd64.tar.gz` |
| Linux ARM64（树莓派） | `home-presence-linux-arm64.tar.gz` |
| macOS Apple Silicon | `home-presence-darwin-arm64.tar.gz` |
| macOS Intel | `home-presence-darwin-amd64.tar.gz` |

```bash
tar xzf home-presence-linux-amd64.tar.gz
sudo ./home-presence-linux-amd64 -role both -home 家庭A -port 8080
```

### 方式 3：交叉编译部署到远程机器

```bash
# 编译 Linux ARM64（树莓派等）
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o home-presence .

# 编译 Linux AMD64（NAS/服务器）
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o home-presence .

# 拷贝到目标机器
scp home-presence user@192.168.1.100:~/
ssh user@192.168.1.100 "sudo ~/home-presence -role scanner -home 家庭B -central http://10.8.0.2:8080"
```

### 开机自启（systemd）

```bash
sudo tee /etc/systemd/system/home-presence.service << 'EOF'
[Unit]
Description=Home Presence Monitor
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/home-presence -role both -home 家庭A -port 8080
WorkingDirectory=/opt/home-presence
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo cp home-presence /usr/local/bin/
sudo mkdir -p /opt/home-presence/data
sudo systemctl enable --now home-presence
```

## 架构

采用服务端/客户端架构：每个家庭部署一个 **Scanner（客户端）**，扫描本地局域网设备后上报到 **Server（服务端）**。

```
家庭A (Scanner)  ──POST /api/report──▶  Server (Web UI + API)
家庭B (Scanner)  ──POST /api/report──▶  Server
```

### 运行模式

| 模式 | 说明 | 示例 |
|------|------|------|
| `server` | 仅服务端：Web UI + 接收上报 | `./home-presence -role server -port 8080` |
| `scanner` | 仅客户端：扫描局域网，上报到服务端 | `./home-presence -role scanner -home 家庭B -central http://server:8080` |
| `both` | 单机模式：同时运行服务端和扫描器 | `./home-presence -role both -home 家庭A -port 8080` |

### 多家庭部署

```bash
# 机器 1：服务端（也扫描本地网络）
./home-presence -role both -home 家庭A -port 8080

# 机器 2：家庭 B 的扫描节点
./home-presence -role scanner -home 家庭B -central http://10.8.0.2:8080

# 机器 3：家庭 C 的扫描节点
./home-presence -role scanner -home 家庭C -central http://10.8.0.2:8080
```

## 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-port` | 8080 | HTTP 服务端口 |
| `-home` | Home | 家庭名称 |
| `-role` | both | 运行角色：`both` / `server` / `scanner` |
| `-central` | - | 服务端地址（scanner 模式必填） |
| `-subnet` | 自动检测 | 扫描的网段（如 `192.168.1.0/24`） |
| `-interval` | 30000 | 扫描间隔（毫秒） |

## 设备管理

### Web UI

1. 打开 http://localhost:8080
2. 点击家庭卡片进入设备列表
3. 点击设备卡片编辑名称和归属人

### API

```bash
# 获取所有设备和统计
curl http://localhost:8080/api/devices

# 更新设备信息（MAC 使用 - 分隔大写格式）
curl -X PUT http://localhost:8080/api/devices/AA-BB-CC-DD-EE-FF \
  -H "Content-Type: application/json" \
  -d '{"name":"iPhone 15","owner":"张三","category":"phone"}'

# 可选 category 值：phone, laptop, desktop, tablet, tv, speaker, watch, printer, router, nas, iot, unknown
# 留空则恢复自动检测
```

## 扫描原理

1. **Ping Sweep** — UDP 探测子网内所有 IP，触发 ARP 解析
2. **ARP 表** — 读取系统 ARP 缓存，获取局域网内活跃设备的 IP 和 MAC
3. **并发探测** — 同时执行以下三种探测：
   - **NetBIOS 查询** — 调用 `nmblookup` 获取设备名称（10 并发，3s 超时）
   - **mDNS 服务发现** — 浏览 10 种服务类型（打印机、AirPlay、Chromecast 等），5s 超时
   - **DNS PTR 反向查找** — 解析 IP 对应的主机名（20 并发，10 分钟缓存）
4. **OUI 厂商识别** — 通过 MAC 前缀匹配 IEEE OUI 数据库（38,899 厂商）
5. **Hostname 启发式** — 30 种正则模式匹配设备名称（iPhone/MacBook/Galaxy 等）
6. **优先级融合** — 按信号优先级（mDNS > hostname > vendor hint）确定最终设备类别

## 数据存储

设备信息持久化在 `data/devices.json`，采用原子写入（写临时文件后 rename），程序收到 SIGINT/SIGTERM 时自动保存。

## License

MIT
