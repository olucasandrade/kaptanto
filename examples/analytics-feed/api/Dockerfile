FROM node:22-alpine

WORKDIR /app

COPY analytics-feed/api/package.json ./package.json
COPY analytics-feed/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY analytics-feed/api/src ./src
COPY shared/src /shared/src

EXPOSE 4003

CMD ["npm", "run", "start"]
