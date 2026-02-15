/**
 * 设备状态管理器
 * 
 * 存储结构：
 * {
 *   "家庭A": {
 *     "AA:BB:CC:DD:EE:FF": {
 *       mac: "AA:BB:CC:DD:EE:FF",
 *       ip: "10.8.0.5",
 *       name: "张老板 iPhone",
 *       owner: "张老板",
 *       home: "家庭A",
 *       known: true,
 *       online: true,
 *       lastSeen: 1707899999999
 *     }
 *   }
 * }
 */

// 内存存储（实际项目可改用 SQLite）
const deviceState = {};

/**
 * 初始化家庭
 */
function initHome(homeName) {
  if (!deviceState[homeName]) {
    deviceState[homeName] = {};
  }
  return deviceState[homeName];
}

/**
 * 更新设备状态
 */
function updateDevices(homeName, devices) {
  const home = initHome(homeName);
  const changes = [];
  const now = Date.now();

  // 标记所有设备为离线
  for (const mac in home) {
    home[mac].online = false;
  }

  // 更新在线设备
  for (const device of devices) {
    const mac = device.mac.toUpperCase();
    const existing = home[mac];
    
    if (existing) {
      // 设备已存在
      if (!existing.online) {
        // 设备重新上线
        changes.push({
          type: 'online',
          device: existing
        });
      }
      existing.online = true;
      existing.lastSeen = now;
      existing.ip = device.ip;
      existing.name = device.name || existing.name;
    } else {
      // 新设备
      const newDevice = {
        mac,
        ip: device.ip,
        name: device.name || null,
        owner: null,
        home: homeName,
        known: false,
        online: true,
        firstSeen: now,
        lastSeen: now
      };
      home[mac] = newDevice;
      changes.push({
        type: 'new',
        device: newDevice
      });
    }
  }

  // 检查离线设备
  for (const mac in home) {
    const dev = home[mac];
    // 5分钟前的设备视为离线
    if (!dev.online && now - dev.lastSeen < 5 * 60 * 1000) {
      // 刚变成离线
      if (dev.lastSeen && now - dev.lastSeen >= 5 * 60 * 1000) {
        changes.push({
          type: 'offline',
          device: dev
        });
      }
    }
  }

  return changes;
}

/**
 * 获取所有设备状态
 */
function getAllDevices() {
  const result = [];
  for (const homeName in deviceState) {
    for (const mac in deviceState[homeName]) {
      const dev = deviceState[homeName][mac];
      result.push(dev);
    }
  }
  return result;
}

/**
 * 获取指定家庭设备
 */
function getHomeDevices(homeName) {
  return deviceState[homeName] || {};
}

/**
 * 更新设备信息
 */
function updateDeviceInfo(homeName, mac, info) {
  const home = deviceState[homeName];
  if (!home) return null;
  
  const upperMac = mac.toUpperCase();
  const device = home[upperMac];
  if (!device) return null;

  if (info.name) device.name = info.name;
  if (info.owner) device.owner = info.owner;
  if (info.known !== undefined) device.known = info.known;

  return device;
}

/**
 * 获取家庭在线统计
 */
function getHomeStats() {
  const stats = {};
  for (const homeName in deviceState) {
    const home = deviceState[homeName];
    let online = 0;
    let known = 0;
    for (const mac in home) {
      if (home[mac].online) {
        online++;
        if (home[mac].known) known++;
      }
    }
    stats[homeName] = { online, known, total: Object.keys(home).length };
  }
  return stats;
}

module.exports = {
  updateDevices,
  getAllDevices,
  getHomeDevices,
  updateDeviceInfo,
  getHomeStats,
  initHome
};
