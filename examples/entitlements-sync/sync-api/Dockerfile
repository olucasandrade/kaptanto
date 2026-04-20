FROM node:22-alpine

WORKDIR /app

COPY entitlements-sync/sync-api/package.json ./package.json
COPY entitlements-sync/sync-api/tsconfig.json ./tsconfig.json
RUN npm install

COPY entitlements-sync/sync-api/src ./src
COPY shared/src /shared/src

EXPOSE 4012

CMD ["npm", "run", "start"]

