FROM node:22-alpine

WORKDIR /app

COPY supporting/notifications/api/package.json ./package.json
COPY supporting/notifications/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY supporting/notifications/api/src ./src
COPY shared/src /shared/src

EXPOSE 4001

CMD ["npm", "run", "start"]
