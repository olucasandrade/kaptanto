FROM node:22-alpine

WORKDIR /app

COPY demos/audit-trail/api/package.json ./package.json
COPY demos/audit-trail/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY demos/audit-trail/api/src ./src
COPY shared/src /shared/src

EXPOSE 4007

CMD ["npm", "run", "start"]
