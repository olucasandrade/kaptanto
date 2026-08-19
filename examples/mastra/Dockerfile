FROM node:22-alpine

WORKDIR /app

COPY examples/mastra/package.json examples/mastra/tsconfig.json ./
COPY examples/mastra/src ./src

RUN npm install

CMD ["npm", "start"]
