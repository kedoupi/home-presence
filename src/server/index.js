/**
 * 中央服务
 * 
 * 提供：
 * - /api/report - 接收扫描器上报
 * - /api/devices - 设备管理
 * - / - Web 管理界面
 */

const express = require('express');
const path = require('path');
const schedule = require('node-schedule');

const { loadConfig, getDevices, saveDevices, getHomes } = require('../shared/config');
const deviceState = require('../shared/deviceState');
const telegram = require('../shared/telegram');

const app = express();
app.use(express.json());
app.use(express.static(path.join(__dirname, '../../public')));

// 中间件：初始化
app.use((req, res, next) => {
  const homes = getHomes();
  for (const homeName in homes) {
    deviceState.initHome(homeName);
  }
  next();
});

// ==================== API 接口 ====================

/**
 * POST /api/report
 * 扫描器上报设备
 */
app.post('/api/report', async (req, res) => {
  const { home, timestamp, devices } = req.body;
  
  if (!home || !Array.isArray(devices)) {
    return res.status(400).json({ error: 'Invalid request' });
  }
  
  console.log('📥 收到 ' + home + ' 上报: ' + devices.length + ' 个设备');
  
  // 更新状态
  const changes = deviceState.updateDevices(home, devices);
  
  // 处理变化并推送
  for (const change of changes) {
    const device = change.device;
    
    if (change.type === 'online') {
      if (device.known) {
        await telegram.notifyOnline(device);
      }
    } else if (change.type === 'offline') {
      if (device.known) {
        await telegram.notifyOffline(device);
      }
    } else if (change.type === 'new' && !device.known) {
      await telegram.notifyNewDevice(device);
    }
  }
  
  res.json({ success: true, changes: changes.length });
});

/**
 * GET /api/devices
 * 获取所有设备
 */
app.get('/api/devices', (req, res) => {
  const devices = deviceState.getAllDevices();
  const stats = deviceState.getHomeStats();
  const configDevices = getDevices();
  
  const result = devices.map(d => {
    const config = configDevices[d.mac] || {};
    return {
      ...d,
      name: d.name || config.name || null,
      owner: d.owner || config.owner || null,
      known: d.known || !!config.owner
    };
  });
  
  res.json({
    devices: result,
    stats
  });
});

/**
 * GET /api/devices/:mac
 * 获取单个设备
 */
app.get('/api/devices/:mac', (req, res) => {
  const mac = req.params.mac.toUpperCase();
  const devices = deviceState.getAllDevices();
  const device = devices.find(d => d.mac === mac);
  
  if (!device) {
    return res.status(404).json({ error: 'Device not found' });
  }
  
  res.json(device);
});

/**
 * PUT /api/devices/:mac
 * 更新设备信息
 */
app.put('/api/devices/:mac', (req, res) => {
  const mac = req.params.mac.toUpperCase();
  const { name, owner, known, home } = req.body;
  
  const device = deviceState.updateDeviceInfo(home || '家庭A', mac, { name, owner, known });
  
  if (!device) {
    return res.status(404).json({ error: 'Device not found' });
  }
  
  const devices = getDevices();
  devices[mac] = {
    ...devices[mac],
    name: name || devices[mac]?.name,
    owner: owner || devices[mac]?.owner,
    known: known ?? devices[mac]?.known ?? false,
    home: home || devices[mac]?.home
  };
  saveDevices(devices);
  
  res.json({ success: true, device });
});

/**
 * GET /api/config
 * 获取配置
 */
app.get('/api/config', (req, res) => {
  const config = loadConfig();
  res.json({
    homes: Object.keys(config.homes || {}),
    telegram: config.telegram?.enabled || false
  });
});

// ==================== Web UI ====================

/**
 * 主页面
 */
app.get('/', (req, res) => {
  const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>家庭设备监控</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #f5f5f5;
      padding: 20px;
    }
    .container { max-width: 1200px; margin: 0 auto; }
    h1 { text-align: center; margin-bottom: 30px; color: #333; }
    
    .stats {
      display: flex;
      gap: 20px;
      margin-bottom: 30px;
      justify-content: center;
    }
    .stat-card {
      background: white;
      padding: 20px 30px;
      border-radius: 12px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.1);
      text-align: center;
    }
    .stat-card .home-name { font-size: 14px; color: #666; }
    .stat-card .count { font-size: 36px; font-weight: bold; color: #2563eb; }
    .stat-card .label { font-size: 12px; color: #999; }
    
    .home-section {
      background: white;
      border-radius: 12px;
      padding: 20px;
      margin-bottom: 20px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.1);
    }
    .home-section h2 {
      margin-bottom: 15px;
      padding-bottom: 10px;
      border-bottom: 1px solid #eee;
    }
    
    .device-list {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: 12px;
    }
    .device-card {
      padding: 15px;
      border-radius: 8px;
      background: #fafafa;
      border: 1px solid #eee;
      cursor: pointer;
      transition: all 0.2s;
    }
    .device-card:hover { border-color: #2563eb; }
    .device-card.online { border-left: 4px solid #22c55e; }
    .device-card.offline { border-left: 4px solid #999; opacity: 0.6; }
    .device-card.unknown { border-left: 4px solid #f59e0b; }
    
    .device-header { display: flex; justify-content: space-between; align-items: center; }
    .device-name { font-weight: 600; color: #333; }
    .device-status {
      font-size: 12px;
      padding: 2px 8px;
      border-radius: 10px;
    }
    .status-online { background: #dcfce7; color: #166534; }
    .status-offline { background: #f3f4f6; color: #666; }
    
    .device-info { margin-top: 10px; font-size: 13px; color: #666; }
    .device-info div { margin: 4px 0; }
    .device-owner { color: #2563eb; font-weight: 500; }
    
    .empty { text-align: center; padding: 40px; color: #999; }
    
    .modal {
      display: none;
      position: fixed;
      top: 0; left: 0; right: 0; bottom: 0;
      background: rgba(0,0,0,0.5);
      align-items: center;
      justify-content: center;
    }
    .modal.show { display: flex; }
    .modal-content {
      background: white;
      padding: 30px;
      border-radius: 12px;
      width: 400px;
      max-width: 90%;
    }
    .modal h3 { margin-bottom: 20px; }
    .form-group { margin-bottom: 15px; }
    .form-group label { display: block; margin-bottom: 5px; color: #666; }
    .form-group input {
      width: 100%;
      padding: 10px;
      border: 1px solid #ddd;
      border-radius: 6px;
    }
    .btn {
      padding: 10px 20px;
      border: none;
      border-radius: 6px;
      cursor: pointer;
      font-size: 14px;
    }
    .btn-primary { background: #2563eb; color: white; }
    .btn-secondary { background: #f3f4f6; color: #666; margin-left: 10px; }
  </style>
</head>
<body>
  <div class="container">
    <h1>家庭设备监控</h1>
    
    <div class="stats" id="stats"></div>
    
    <div id="deviceList"></div>
  </div>
  
  <div class="modal" id="editModal">
    <div class="modal-content">
      <h3>编辑设备</h3>
      <div class="form-group">
        <label>MAC 地址</label>
        <input type="text" id="editMac" readonly>
      </div>
      <div class="form-group">
        <label>设备名称</label>
        <input type="text" id="editName" placeholder="如：张老板 iPhone">
      </div>
      <div class="form-group">
        <label>所属人</label>
        <input type="text" id="editOwner" placeholder="如：张老板">
      </div>
      <div class="form-group">
        <label>家庭</label>
        <input type="text" id="editHome" readonly>
      </div>
      <button class="btn btn-primary" onclick="saveDevice()">保存</button>
      <button class="btn btn-secondary" onclick="closeModal()">取消</button>
    </div>
  </div>
  
  <script>
    let currentDevices = [];
    let editingMac = '';
    
    async function loadData() {
      const [devicesRes, configRes] = await Promise.all([
        fetch('/api/devices'),
        fetch('/api/config')
      ]);
      
      const { devices, stats } = await devicesRes.json();
      const { homes } = await configRes.json();
      
      currentDevices = devices;
      
      const statsEl = document.getElementById('stats');
      statsEl.innerHTML = homes.map(home => {
        const s = stats[home] || { online: 0, known: 0 };
        return '<div class="stat-card">' +
          '<div class="home-name">' + home + '</div>' +
          '<div class="count">' + s.online + '</div>' +
          '<div class="label">在线 / 共 ' + s.total + ' 台</div>' +
        '</div>';
      }).join('');
      
      const listEl = document.getElementById('deviceList');
      listEl.innerHTML = homes.map(home => {
        const homeDevices = devices.filter(d => d.home === home);
        const onlineDevices = homeDevices.filter(d => d.online);
        
        if (homeDevices.length === 0) {
          return '<div class="home-section"><h2>' + home + '</h2><div class="empty">暂无设备数据</div></div>';
        }
        
        let cards = homeDevices.map(d => {
          const statusClass = d.online ? 'online' : 'offline';
          const statusText = d.online ? '在线' : '离线';
          const unknownClass = !d.known ? 'unknown' : '';
          const name = d.name || '未知设备';
          const ownerHtml = d.owner ? '<div class="device-owner">' + d.owner + '</div>' : '';
          
          return '<div class="device-card ' + statusClass + ' ' + unknownClass + '" onclick="editDevice(\\'' + d.mac + '\\')">' +
            '<div class="device-header">' +
              '<span class="device-name">' + name + '</span>' +
              '<span class="device-status status-' + (d.online ? 'online' : 'offline') + '">' + statusText + '</span>' +
            '</div>' +
            '<div class="device-info">' +
              '<div>IP: ' + d.ip + '</div>' +
              '<div>MAC: ' + d.mac + '</div>' +
              ownerHtml +
            '</div>' +
          '</div>';
        }).join('');
        
        return '<div class="home-section"><h2>' + home + ' <small style="font-weight:normal;color:#666">(' + onlineDevices.length + ' 在线)</small></h2><div class="device-list">' + cards + '</div></div>';
      }).join('');
    }
    
    function editDevice(mac) {
      const device = currentDevices.find(d => d.mac === mac);
      if (!device) return;
      
      editingMac = mac;
      document.getElementById('editMac').value = device.mac;
      document.getElementById('editName').value = device.name || '';
      document.getElementById('editOwner').value = device.owner || '';
      document.getElementById('editHome').value = device.home;
      document.getElementById('editModal').classList.add('show');
    }
    
    function closeModal() {
      document.getElementById('editModal').classList.remove('show');
    }
    
    async function saveDevice() {
      const name = document.getElementById('editName').value;
      const owner = document.getElementById('editOwner').value;
      const home = document.getElementById('editHome').value;
      
      await fetch('/api/devices/' + editingMac, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, owner, known: !!owner, home })
      });
      
      closeModal();
      loadData();
    }
    
    loadData();
    setInterval(loadData, 10000);
  </script>
</body>
</html>`;
  
  res.send(html);
});

// ==================== 定时任务 ====================

// 每小时汇总
schedule.scheduleJob('0 * * * *', async () => {
  const stats = deviceState.getHomeStats();
  const devices = deviceState.getAllDevices();
  await telegram.notifySummary(stats, devices);
});

// ==================== 启动 ====================

function main() {
  const config = loadConfig();
  const port = config.server.port || 3000;
  const host = config.server.host || '0.0.0.0';
  
  // 初始化 Telegram
  telegram.initTelegram();
  
  // 初始化家庭
  const homes = getHomes();
  for (const homeName in homes) {
    deviceState.initHome(homeName);
  }
  
  app.listen(port, host, () => {
    console.log('🏠 中央服务启动: http://' + host + ':' + port);
    console.log('   Web UI: http://' + host + ':' + port);
  });
}

main();
