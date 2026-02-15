/**
 * 配置加载器
 */
const fs = require('fs');
const path = require('path');
const YAML = require('yaml');

const CONFIG_PATH = process.env.CONFIG_PATH || path.join(__dirname, '../../config.yaml');

let config = null;

/**
 * 加载配置文件
 */
function loadConfig() {
  if (config) return config;
  
  try {
    const content = fs.readFileSync(CONFIG_PATH, 'utf-8');
    config = YAML.parse(content);
    return config;
  } catch (err) {
    console.error('❌ 加载配置文件失败:', err.message);
    return getDefaultConfig();
  }
}

/**
 * 获取默认配置
 */
function getDefaultConfig() {
  return {
    server: {
      host: '0.0.0.0',
      port: parseInt(process.env.PORT) || 3000
    },
    telegram: {
      enabled: false
    },
    homes: {
      '家庭A': {
        scanner_enabled: true,
        subnet: process.env.SCAN_SUBNET || '10.8.0.0/24'
      }
    },
    devices: {}
  };
}

/**
 * 获取设备映射
 */
function getDevices() {
  const cfg = loadConfig();
  return cfg.devices || {};
}

/**
 * 保存设备映射
 */
function saveDevices(devices) {
  try {
    const content = fs.readFileSync(CONFIG_PATH, 'utf-8');
    const cfg = YAML.parse(content);
    cfg.devices = devices;
    fs.writeFileSync(CONFIG_PATH, YAML.stringify(cfg));
    config = cfg;
    return true;
  } catch (err) {
    console.error('❌ 保存设备配置失败:', err.message);
    return false;
  }
}

/**
 * 获取家庭配置
 */
function getHomes() {
  const cfg = loadConfig();
  return cfg.homes || {};
}

module.exports = {
  loadConfig,
  getDevices,
  saveDevices,
  getHomes
};
