FROM node:22-alpine

WORKDIR /app

COPY demos/audit-trail/web/package.json ./package.json
COPY demos/audit-trail/web/tsconfig.json ./tsconfig.json
COPY demos/audit-trail/web/vite.config.ts ./vite.config.ts
COPY demos/audit-trail/web/index.html ./index.html
RUN npm install

COPY demos/audit-trail/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
