# Home Presence

家庭局域网设备感知系统 — 实时监控多个家庭的设备在线状态。

## 功能特性

- 🌐 **多家庭支持** — 同时监控多个地点的局域网设备
- 📱 **设备识别** — MAC 地址白名单，识别是谁的设备
- 🔔 **状态推送** — 设备上线/离线时 Telegram 推送
- 📊 **Web 管理界面** — 可视化查看各家庭设备、编辑映射
- 📝 **历史记录** — 查看谁什么时候回家/离家

## 快速开始

### 1. 克隆项目

```bash
git clone https://github.com/kedoupi/home-presence.git
cd home-presence
```

### 2. 配置

复制并编辑配置文件：

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，配置：

```yaml
# 服务配置
server:
  host: "0.0.0.0"
  port: 3000

# Telegram 推送
telegram:
  enabled: true
  bot_token: "YOUR_BOT_TOKEN"
  chat_id: "YOUR_CHAT_ID"

# 家庭配置
homes:
  家庭A:
    scanner_enabled: true    # 是否在此机器运行扫描器
    subnet: "10.8.0.0/24"   # 扫描的网段
    devices: []
  
  家庭B:
    scanner_enabled: false   # 家庭B的扫描器运行在其他机器
    devices: []

# 设备映射（首次运行后会自动填充未知设备）
devices: {}
```

### 3. 启动

```bash
# 方式一：Docker（推荐）
docker compose up -d

# 方式二：Node.js 直接运行
npm install
npm run dev
```

### 4. 访问 Web UI

打开 http://localhost:3000 查看设备状态。

---

## 多机器部署

### 中央服务（家庭A，IP: 10.8.0.2）

运行扫描器 + 中央服务 + Web UI：

```yaml
# config.yaml
homes:
  家庭A:
    scanner_enabled: true
    subnet: "10.8.0.0/24"
```

启动后访问 http://10.8.0.2:3000

### 扫描器节点（家庭B，IP: 10.8.0.3）

只运行扫描器，上报到中央服务：

```bash
# 方式一：Docker
docker run -d \
  --network host \
  -e CENTRAL_URL=http://10.8.0.2:3000 \
  -e HOME_NAME=家庭B \
  -e SCAN_SUBNET=10.8.0.0/24 \
  home-presence-scanner

# 方式二：Node.js
SCAN_SUBNET=10.8.0.0/24 HOME_NAME=家庭B CENTRAL_URL=http://10.8.0.2:3000 npm run scanner
```

---

## 设备映射

### 方式一：Web UI 编辑

1. 打开 http://localhost:3000
2. 点击未知设备
3. 填写名称和所属人

### 方式二：直接编辑配置

```yaml
devices:
  "AA:BB:CC:DD:EE:01":
    name: "张老板 iPhone"
    owner: "张老板"
    home: 家庭A
```

---

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | /api/report | 扫描器上报设备 |
| GET | /api/devices | 获取所有设备状态 |
| PUT | /api/devices/:mac | 更新设备信息 |
| GET | / | Web 管理界面 |

---

## 依赖

- Node.js 18+
- arp-scan（系统依赖，扫描局域网）
- Docker & Docker Compose（可选）

### 安装 arp-scan

```bash
# macOS
brew install arp-scan

# Ubuntu/Debian
sudo apt install arp-scan

# CentOS/RHEL
sudo yum install arp-scan
```

---

## 目录结构

```
home-presence/
├── src/
│   ├── scanner/        # 设备扫描器
│   ├── server/         # 中央服务 + Web UI
│   └── shared/         # 共享类型
├── public/             # 静态资源
├── config.example.yaml # 配置示例
├── Dockerfile
├── docker-compose.yaml
└── package.json
```

---

## 未来计划

- [ ] 自动发现家庭（通过 mDNS/Tailscale API）
- [ ] 微信推送支持
- [ ] 设备指纹识别（根据 MAC 厂商前缀猜设备类型）
- [ ] 历史数据图表

---

## License

MIT
