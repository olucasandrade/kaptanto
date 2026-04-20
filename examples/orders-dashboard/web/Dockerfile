FROM node:22-alpine

WORKDIR /app

COPY orders-dashboard/web/package.json ./package.json
COPY orders-dashboard/web/tsconfig.json ./tsconfig.json
COPY orders-dashboard/web/vite.config.ts ./vite.config.ts
COPY orders-dashboard/web/index.html ./index.html
RUN npm install

COPY orders-dashboard/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
