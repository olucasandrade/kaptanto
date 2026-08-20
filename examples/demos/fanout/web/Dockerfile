FROM node:22-alpine

WORKDIR /app

COPY demos/fanout/web/package.json ./package.json
COPY demos/fanout/web/tsconfig.json ./tsconfig.json
COPY demos/fanout/web/vite.config.ts ./vite.config.ts
COPY demos/fanout/web/index.html ./index.html
RUN npm install

COPY demos/fanout/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
