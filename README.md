# Home Presence

家庭局域网设备感知系统 — 实时监控多个家庭的设备在线状态。

## 功能特性

- 🌐 **多家庭支持** — 同时监控多个地点的局域网设备
- 📱 **设备识别** — MAC 地址白名单，识别是谁的设备
- 🔔 **状态推送** — 设备上线/离线时 Telegram 推送
- 📊 **Web 管理界面** — 可视化查看各家庭设备、编辑映射
- ⚡ **单二进制文件** — 无需安装任何依赖

## 快速开始

### 1. 下载二进制

```bash
# macOS ARM (Apple Silicon)
curl -L -o home-presence https://github.com/kedoupi/home-presence/releases/download/v1.0.0/home-presence

# macOS Intel
curl -L -o home-presence https://github.com/kedoupi/home-presence/releases/download/v1.0.0/home-presence

# Linux
curl -L -o home-presence https://github.com/kedoupi/home-presence/releases/download/v1.0.0/home-presence
```

```bash
chmod +x home-presence
```

### 2. 运行

```bash
# 中央服务 + 扫描器（单台机器）
./home-presence -role both -home 家庭A -port 8080
```

### 3. 访问 Web UI

打开 http://localhost:8080 查看设备状态。

---

## 命令行参数

| 参数 | 缩写 | 默认值 | 说明 |
|------|------|--------|------|
| `-port` | - | 8080 | HTTP 服务端口 |
| `-home` | - | 家庭A | 家庭名称 |
| `-role` | - | both | 运行角色：both/server/scanner |
| `-central` | - | - | 中央服务地址（scanner 模式使用） |
| `-subnet` | - | 自动检测 | 扫描的网段 |
| `-interval` | - | 30000 | 扫描间隔（毫秒） |

---

## 多机器部署

### 中央服务（家庭A）

运行扫描器 + 中央服务 + Web UI：

```bash
./home-presence -role both -home 家庭A -port 8080
```

### 扫描器节点（家庭B）

只运行扫描器，上报到中央服务：

```bash
./home-presence -role scanner -home 家庭B -central http://10.8.0.2:8080
```

---

## 设备映射

### 方式一：Web UI 编辑

1. 打开 http://localhost:8080
2. 点击未知设备
3. 填写名称和所属人

### 方式二：API

```bash
# 获取所有设备
curl http://localhost:8080/api/devices

# 更新设备信息
curl -X PUT http://localhost:8080/api/devices/XX:XX:XX:XX:XX:XX \
  -H "Content-Type: application/json" \
  -d '{"name":"张老板 iPhone","owner":"张老板"}'
```

---

## 扫描原理

1. **ARP 表**：优先读取系统 ARP 表（最快）
2. **Ping 扫描**：ARP 为空时，使用 ping 扫描整个网段
3. **自动检测**：自动识别本机所在的局域网网段

---

## Telegram 推送（可选）

编辑 `data/devices.json` 添加 Telegram 配置，或通过 Web UI 界面配置。

---

## 数据存储

设备信息保存在 `data/devices.json`，程序退出时会自动保存。

---

## License

MIT
