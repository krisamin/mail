# mail-web — React Router 웹 UI (메일·설정·관리)
# 빌드는 bun (빠름), 런타임은 node (react-router-serve가 node 스트림 기반 —
# bun의 react-dom/server.bun엔 renderToPipeableStream이 없어 기동 실패).
FROM oven/bun:1.4-alpine AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build && bun install --frozen-lockfile --production

FROM node:24-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build --chown=node:node --chmod=644 /app/package.json ./
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/build ./build
USER node
EXPOSE 3000
CMD ["node", "node_modules/@react-router/serve/bin.cjs", "./build/server/index.js"]
