# mail-web — RR7 관리/셀프서비스 UI
# 빌드는 bun (빠름), 런타임은 node (react-router-serve가 node 스트림 기반 —
# bun의 react-dom/server.bun엔 renderToPipeableStream이 없어 기동 실패).
FROM oven/bun:1.3-alpine AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build && bun install --frozen-lockfile --production

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build --chown=node:node --chmod=644 /app/package.json ./
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/build ./build
USER node
EXPOSE 3000
CMD ["node", "node_modules/@react-router/serve/bin.js", "./build/server/index.js"]
