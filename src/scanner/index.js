/**
 * 设备扫描器
 * 
 * 使用 arp-scan 扫描局域网设备
 * 
 * 环境变量：
 * - SCAN_SUBNET: 扫描的子网（如 10.8.0.0/24）
 * - HOME_NAME: 家庭名称
 * - CENTRAL_URL: 中央服务地址（可选，不填则只输出到 stdout）
 * - SCAN_INTERVAL: 扫描间隔（毫秒），默认 10000ms
 */

const { exec } = require('child_process');
const os = require('os');
const axios = require('axios');

const SCAN_SUBNET = process.env.SCAN_SUBNET;  // 可选，不填则自动检测
const HOME_NAME = process.env.HOME_NAME || '家庭A';
const CENTRAL_URL = process.env.CENTRAL_URL || null;
const SCAN_INTERVAL = parseInt(process.env.SCAN_INTERVAL) || 10000;

/**
 * 自动检测本机所在网段
 */
function detectSubnet() {
  const interfaces = os.networkInterfaces();
  
  for (const name of Object.keys(interfaces)) {
    for (const info of interfaces[name]) {
      // 跳过 IPv6 和内部地址
      if (info.family !== 'IPv4' || info.internal) continue;
      
      // 跳过 Docker/VPN 接口（通常有 Docker 或 tun/tap 前缀）
      if (name.includes('docker') || name.includes('tun') || name.includes('tap') || name.includes('utun')) continue;
      
      // 从 IP 和子网掩码计算网段
      const ip = info.address;
      const mask = info.netmask;
      const subnet = ipToSubnet(ip, mask);
      
      if (subnet) {
        console.log(`🔍 自动检测到网段: ${subnet} (接口: ${name})`);
        return subnet;
      }
    }
  }
  
  console.warn('⚠️ 无法自动检测网段，使用默认值');
  return '10.8.0.0/24';
}

/**
 * IP + 子网掩码 -> 网段
 */
function ipToSubnet(ip, mask) {
  const ipParts = ip.split('.').map(Number);
  const maskParts = mask.split('.').map(Number);
  
  const network = ipParts.map((p, i) => p & maskParts[i]).join('.');
  return `${network}/24`; // 简化处理，假设都是 /24
}

/**
 * 解析 arp-scan 输出
 */
function parseArpScan(output) {
  const devices = [];
  const lines = output.split('\n');
  
  for (const line of lines) {
    // 跳过空行和标题行
    if (!line.trim() || line.includes('Starting') || line.includes('packets') || line.includes('IP address')) {
      continue;
    }
    
    // 格式: 10.8.0.5    aa:bb:cc:dd:ee:ff   厂商名称
    const match = line.match(/^([\\d.]+)\\s+([0-9a-fA-F:]+)/);
    if (match) {
      const ip = match[1];
      const mac = match[2].toUpperCase();
      
      // 过滤掉广播地址
      if (mac === 'FF:FF:FF:FF:FF:FF') continue;
      
      devices.push({ ip, mac, name: null });
    }
  }
  
  return devices;
}

/**
 * 执行扫描
 */
function scan() {
  return new Promise((resolve, reject) => {
    const cmd = `arp-scan -l --plain --quiet --interface=$(route -n get default | grep interface | awk '{print $2}')`;
    
    exec(cmd, { timeout: 30000 }, (err, stdout, stderr) => {
      if (err) {
        // 尝试不带 interface 参数（自动选择）
        exec('arp-scan -l --plain --quiet', { timeout: 30000 }, (err2, stdout2, stderr2) => {
          if (err2) {
            console.error('❌ 扫描失败:', err2.message);
            resolve([]);
          } else {
            resolve(parseArpScan(stdout2));
          }
        });
      } else {
        resolve(parseArpScan(stdout));
      }
    });
  });
}

/**
 * 上报中央服务
 */
async function reportToCentral(devices) {
  if (!CENTRAL_URL) return;
  
  try {
    await axios.post(`${CENTRAL_URL}/api/report`, {
      home: HOME_NAME,
      timestamp: Date.now(),
      devices
    }, {
      timeout: 5000
    });
    console.log(`📤 已上报 ${devices.length} 个设备到 ${CENTRAL_URL}`);
  } catch (err) {
    console.error('❌ 上报失败:', err.message);
  }
}

/**
 * 主循环
 */
async function main() {
  // 自动检测网段（如果未指定）
  const subnet = SCAN_SUBNET || detectSubnet();
  
  console.log('🏠 设备扫描器启动');
  console.log(`   家庭: ${HOME_NAME}`);
  console.log(`   网段: ${subnet}`);
  console.log(`   目标: ${CENTRAL_URL || '本地输出'}`);
  console.log(`   间隔: ${SCAN_INTERVAL}ms`);
  console.log('');
  
  // 立即执行一次
  await runScan();
  
  // 定时扫描
  setInterval(runScan, SCAN_INTERVAL);
}

/**
 * 执行一次扫描
 */
async function runScan() {
  const now = new Date().toLocaleTimeString();
  console.log(`[${now}] 🔍 扫描 ${HOME_NAME}...`);
  
  const devices = await scan();
  console.log(`   发现 ${devices.length} 个设备`);
  
  if (devices.length > 0) {
    // 打印设备列表
    for (const dev of devices) {
      console.log(`   - ${dev.ip}  ${dev.mac}`);
    }
  }
  
  // 上报
  await reportToCentral(devices);
}

// 启动
main();
