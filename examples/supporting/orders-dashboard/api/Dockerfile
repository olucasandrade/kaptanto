FROM node:22-alpine

WORKDIR /app

COPY supporting/orders-dashboard/api/package.json ./package.json
COPY supporting/orders-dashboard/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY supporting/orders-dashboard/api/src ./src
COPY shared/src /shared/src

EXPOSE 4002

CMD ["npm", "run", "start"]
