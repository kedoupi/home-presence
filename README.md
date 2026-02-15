# Home Presence

家庭局域网设备感知系统 — 实时监控多个家庭的设备在线状态。

## 功能特性

- **多家庭支持** — 服务端/客户端架构，每个家庭部署一个扫描节点
- **设备识别** — 通过 MAC 地址 OUI 前缀自动识别设备类型（Apple/Samsung 等）
- **NetBIOS 名称发现** — 自动获取局域网设备的 NetBIOS 名称
- **Web 管理界面** — 支持 Dark Mode 的响应式 UI，可编辑设备名称和归属人
- **单二进制文件** — Go 编译，无运行时依赖

## 快速开始

### 从源码构建

```bash
git clone https://github.com/kedoupi/home-presence.git
cd home-presence
go build -o home-presence .
```

### 运行

```bash
# 单机模式（扫描器 + 服务端）
./home-presence -role both -home 家庭A -port 8080
```

打开 http://localhost:8080 查看设备状态。

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
| `-home` | 家庭A | 家庭名称 |
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
  -d '{"name":"iPhone 15","owner":"张三"}'
```

## 扫描原理

1. **ARP 表** — 读取系统 ARP 缓存，获取局域网内活跃设备的 IP 和 MAC
2. **NetBIOS 查询** — 对未命名设备调用 `nmblookup` 获取 NetBIOS 名称
3. **OUI 识别** — 通过 MAC 前缀识别设备厂商/类型
4. **自动检测** — 自动识别本机所在的局域网网段

## 数据存储

设备信息持久化在 `data/devices.json`，采用原子写入（写临时文件后 rename），程序收到 SIGINT/SIGTERM 时自动保存。

## License

MIT
