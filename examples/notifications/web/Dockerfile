FROM node:22-alpine

WORKDIR /app

COPY notifications/web/package.json ./package.json
COPY notifications/web/tsconfig.json ./tsconfig.json
COPY notifications/web/vite.config.ts ./vite.config.ts
COPY notifications/web/index.html ./index.html
RUN npm install

COPY notifications/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
