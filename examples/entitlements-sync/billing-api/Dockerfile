FROM node:22-alpine

WORKDIR /app

COPY entitlements-sync/billing-api/package.json ./package.json
COPY entitlements-sync/billing-api/tsconfig.json ./tsconfig.json
RUN npm install

COPY entitlements-sync/billing-api/src ./src

EXPOSE 4010

CMD ["npm", "run", "start"]

