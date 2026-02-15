# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

```bash
go build -o home-presence .          # 编译单二进制（~17MB，含内嵌 OUI 数据库和 Web UI）
go test -v ./...                     # 运行全部测试（27 个）
go test -run TestClassifyDevice -v   # 运行匹配名称的测试
go test -cover ./...                 # 测试覆盖率
go test -race ./...                  # 竞态检测
```

运行需要 root/sudo（ARP 扫描和 mDNS 需要网络权限）：
```bash
sudo ./home-presence -role both -home 家庭A -port 8080
```

## Architecture

单 package `main` 的 Go 项目，编译为单个二进制文件。采用 server/scanner 架构，支持多家庭部署。

### 文件职责

| 文件 | 职责 |
|------|------|
| `main.go` | 入口：flag 解析、信号处理、启动 scanLoop/startServer |
| `device.go` | 数据模型 `Device`、持久化（atomic write）、状态管理、迁移 |
| `scanner.go` | 网络扫描编排：pingSweep → ARP → 并发(NetBIOS + mDNS + DNS PTR) |
| `detection.go` | 分类引擎：OUI 查找、hostname 模式匹配、vendor hint、优先级融合 |
| `mdns.go` | mDNS 服务发现（zeroconf），10 种服务类型，5 分钟缓存 |
| `server.go` | HTTP API（GET/PUT/POST）+ 静态文件服务 |

### 内嵌资源（go:embed）

- `oui.txt` → `ouiData []byte`（detection.go）— IEEE OUI 厂商数据库，38,899 条
- `static/index.html` → `indexHTML []byte`（main.go）— Web UI

### 扫描流程

```
pingSweep(subnet)     → 触发 ARP 解析（UDP 探测，100 并发）
parseArpTable()       → 读取系统 arp -an
                      → 并发执行：
scanNetBIOS(ips)          NetBIOS 名称查询（10 并发，3s 超时）
scanMDNS()                mDNS 服务发现（5s 超时）
lookupDNS(ips)            DNS PTR 反向查找（20 并发，10 分钟缓存）
                      → classifyDevice() 融合所有信号
```

### 设备分类优先级

```
信号源              优先级    说明
────────────────────────────────────
用户手动设置         100      ManualCategory=true，永不被覆盖
mDNS 服务发现       70-95    _ipp._tcp→打印机(95)、_googlecast._tcp→TV(80) 等
Hostname 启发式      80      正则匹配 "iphone"/"macbook"/"galaxy" 等 30 种模式
Vendor hint          50      单品类厂商直接映射（Sonos→Speaker, Synology→NAS）
```

### 并发模型

- **全局锁**：`mu sync.RWMutex` 保护 `deviceStore` 和 `stats`
- **独立锁**：`mdnsCacheMu`（mDNS 缓存）、`dnsCacheMu`（DNS 缓存）
- **信号量模式**：buffered channel 控制并发数（ping 100, NetBIOS 10, DNS 20）
- `getOrCreateDeviceLocked()` 要求调用方已持有 mu；`initDevice()` 内部加锁

### API

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/devices` | GET | 返回所有设备 + 统计（vendor 自动转为短名） |
| `/api/devices/:mac` | PUT | 更新 name/owner/category（category 需通过 isValidCategory 校验） |
| `/api/report` | POST | Scanner 节点上报设备列表 |
| `/` | GET | 内嵌 Web UI |

## Key Patterns

- **原子写入**：saveDevices() 先写临时文件再 rename，防断电数据损坏
- **向后兼容**：loadDevices() 调用 migrateDevices() 将旧 DeviceType 字段迁移为 Vendor+Category
- **MAC 格式**：内部统一为 `AA-BB-CC-DD-EE-FF` 格式（normalizeMac）
- **shortVendor()**：API 响应中将完整厂商名转为短名（"Apple, Inc." → "Apple"）
- **分类不覆盖**：ManualCategory=true 的设备，classifyDevice() 跳过自动分类

## Testing Notes

- `TestMain` 在 detection_test.go 中初始化 OUI 数据库，所有 detection 测试依赖它
- 测试中使用 `FF-00-00-00-00-00` 等不在 OUI 数据库中的 MAC 来避免 vendor 干扰
- `00-00-00-00-00-00` 实际映射为 "XEROX CORPORATION"，不要假设它为空
- 测试修改全局 deviceStore 时，用 `defer func() { deviceStore = origStore }()` 恢复
