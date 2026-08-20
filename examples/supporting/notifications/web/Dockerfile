FROM node:22-alpine

WORKDIR /app

COPY supporting/notifications/web/package.json ./package.json
COPY supporting/notifications/web/tsconfig.json ./tsconfig.json
COPY supporting/notifications/web/vite.config.ts ./vite.config.ts
COPY supporting/notifications/web/index.html ./index.html
RUN npm install

COPY supporting/notifications/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
