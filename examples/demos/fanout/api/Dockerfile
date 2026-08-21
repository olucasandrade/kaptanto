FROM node:22-alpine

WORKDIR /app

COPY demos/fanout/api/package.json ./package.json
COPY demos/fanout/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY demos/fanout/api/src ./src
COPY shared/src /shared/src

EXPOSE 4006

CMD ["npm", "run", "start"]
