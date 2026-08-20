FROM node:22-alpine

WORKDIR /app

COPY examples/ai/mastra/package.json examples/ai/mastra/tsconfig.json ./
COPY examples/ai/mastra/src ./src

RUN npm install

CMD ["npm", "start"]
