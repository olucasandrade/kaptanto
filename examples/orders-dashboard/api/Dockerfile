FROM node:22-alpine

WORKDIR /app

COPY orders-dashboard/api/package.json ./package.json
COPY orders-dashboard/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY orders-dashboard/api/src ./src
COPY shared/src /shared/src

EXPOSE 4002

CMD ["npm", "run", "start"]
