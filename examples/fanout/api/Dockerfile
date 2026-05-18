FROM node:22-alpine

WORKDIR /app

COPY fanout/api/package.json ./package.json
COPY fanout/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY fanout/api/src ./src
COPY shared/src /shared/src

EXPOSE 4006

CMD ["npm", "run", "start"]
