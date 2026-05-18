FROM node:22-alpine

WORKDIR /app

COPY fanout/web/package.json ./package.json
COPY fanout/web/tsconfig.json ./tsconfig.json
COPY fanout/web/vite.config.ts ./vite.config.ts
COPY fanout/web/index.html ./index.html
RUN npm install

COPY fanout/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
