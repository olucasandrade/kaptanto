FROM node:22-alpine

WORKDIR /app

COPY supporting/analytics-feed/web/package.json ./package.json
COPY supporting/analytics-feed/web/tsconfig.json ./tsconfig.json
COPY supporting/analytics-feed/web/vite.config.ts ./vite.config.ts
COPY supporting/analytics-feed/web/index.html ./index.html
RUN npm install

COPY supporting/analytics-feed/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
