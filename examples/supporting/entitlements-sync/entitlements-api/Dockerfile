FROM node:22-alpine

WORKDIR /app

COPY supporting/entitlements-sync/entitlements-api/package.json ./package.json
COPY supporting/entitlements-sync/entitlements-api/tsconfig.json ./tsconfig.json
RUN npm install

COPY supporting/entitlements-sync/entitlements-api/src ./src

EXPOSE 4011

CMD ["npm", "run", "start"]

