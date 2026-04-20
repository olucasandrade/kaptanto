FROM node:22-alpine

WORKDIR /app

COPY notifications/api/package.json ./package.json
COPY notifications/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY notifications/api/src ./src
COPY shared/src /shared/src

EXPOSE 4001

CMD ["npm", "run", "start"]
