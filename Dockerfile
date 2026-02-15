FROM node:18-alpine

# 安装 arp-scan
RUN apk add --no-cache arp-scan

WORKDIR /app

COPY package*.json ./
RUN npm install --production

COPY . .

EXPOSE 3000

CMD ["node", "src/server/index.js"]
