FROM node:22-alpine

WORKDIR /app

COPY entitlements-sync/entitlements-api/package.json ./package.json
COPY entitlements-sync/entitlements-api/tsconfig.json ./tsconfig.json
RUN npm install

COPY entitlements-sync/entitlements-api/src ./src

EXPOSE 4011

CMD ["npm", "run", "start"]

