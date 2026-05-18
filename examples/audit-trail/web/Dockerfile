FROM node:22-alpine

WORKDIR /app

COPY audit-trail/web/package.json ./package.json
COPY audit-trail/web/tsconfig.json ./tsconfig.json
COPY audit-trail/web/vite.config.ts ./vite.config.ts
COPY audit-trail/web/index.html ./index.html
RUN npm install

COPY audit-trail/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
