FROM node:22-alpine

WORKDIR /app

COPY analytics-feed/web/package.json ./package.json
COPY analytics-feed/web/tsconfig.json ./tsconfig.json
COPY analytics-feed/web/vite.config.ts ./vite.config.ts
COPY analytics-feed/web/index.html ./index.html
RUN npm install

COPY analytics-feed/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
