FROM node:22-alpine

WORKDIR /app

COPY supporting/analytics-feed/api/package.json ./package.json
COPY supporting/analytics-feed/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY supporting/analytics-feed/api/src ./src
COPY shared/src /shared/src

EXPOSE 4003

CMD ["npm", "run", "start"]
