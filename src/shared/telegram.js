/**
 * Telegram 推送模块
 */
const axios = require('axios');
const { loadConfig } = require('./config');

let botToken = null;
let chatId = null;
let enabled = false;

/**
 * 初始化 Telegram
 */
function initTelegram() {
  const config = loadConfig();
  if (config.telegram && config.telegram.enabled) {
    botToken = config.telegram.bot_token;
    chatId = config.telegram.chat_id;
    enabled = true;
    console.log('📱 Telegram 推送已启用');
  }
}

/**
 * 发送消息
 */
async function sendMessage(text, parseMode = 'Markdown') {
  if (!enabled) return;
  
  try {
    await axios.post(`https://api.telegram.org/bot${botToken}/sendMessage`, {
      chat_id: chatId,
      text,
      parse_mode: parseMode
    });
  } catch (err) {
    console.error('❌ Telegram 推送失败:', err.response?.data || err.message);
  }
}

/**
 * 设备上线通知
 */
async function notifyOnline(device) {
  const icon = device.known ? '👤' : '❓';
  const name = device.name || device.mac;
  const owner = device.owner ? `(${device.owner})` : '';
  
  await sendMessage(
    `${icon} *${device.home}*\n` +
    `${name} ${owner} 上线了\n` +
    `\`${device.ip}\``
  );
}

/**
 * 设备离线通知
 */
async function notifyOffline(device) {
  const icon = device.known ? '👋' : '💨';
  const name = device.name || device.mac;
  const owner = device.owner ? `(${device.owner})` : '';
  
  await sendMessage(
    `${icon} *${device.home}*\n` +
    `${name} ${owner} 离线了`
  );
}

/**
 * 新设备发现通知
 */
async function notifyNewDevice(device) {
  await sendMessage(
    `🆕 *新设备发现*\n` +
    `家庭: ${device.home}\n` +
    `MAC: \`${device.mac}\`\n` +
    `IP: \`${device.ip}\``
  );
}

/**
 * 定时汇总
 */
async function notifySummary(stats, devices) {
  let text = '📊 *家庭设备状态汇总*\n\n';
  
  for (const homeName in stats) {
    const homeStats = stats[homeName];
    const homeDevices = Object.values(devices).filter(d => d.home === homeName && d.online);
    
    const knownNames = homeDevices
      .filter(d => d.known && d.owner)
      .map(d => d.owner)
      .join('、');
    
    text += `*${homeName}*\n`;
    text += `在线: ${homeStats.online}人`;
    if (knownNames) {
      text += ` (${knownNames})`;
    }
    text += '\n\n';
  }
  
  await sendMessage(text);
}

module.exports = {
  initTelegram,
  sendMessage,
  notifyOnline,
  notifyOffline,
  notifyNewDevice,
  notifySummary
};
