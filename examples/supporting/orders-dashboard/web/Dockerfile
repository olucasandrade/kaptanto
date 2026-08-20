FROM node:22-alpine

WORKDIR /app

COPY supporting/orders-dashboard/web/package.json ./package.json
COPY supporting/orders-dashboard/web/tsconfig.json ./tsconfig.json
COPY supporting/orders-dashboard/web/vite.config.ts ./vite.config.ts
COPY supporting/orders-dashboard/web/index.html ./index.html
RUN npm install

COPY supporting/orders-dashboard/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
