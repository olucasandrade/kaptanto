FROM node:22-alpine

WORKDIR /app

# Build local workspace packages first (dist/ is not committed).
COPY packages/kaptanto-events /packages/kaptanto-events
COPY packages/kaptanto-mastra /packages/kaptanto-mastra

WORKDIR /packages/kaptanto-events
RUN npm ci && npm run build

WORKDIR /packages/kaptanto-mastra
RUN npm ci && npm run build

WORKDIR /app
COPY examples/mastra/package.json examples/mastra/tsconfig.json ./
COPY examples/mastra/src ./src

# Rewrite file: deps to the built packages inside the image.
RUN npm pkg set dependencies.@kaptanto/events=file:/packages/kaptanto-events \
  && npm pkg set dependencies.@kaptanto/mastra=file:/packages/kaptanto-mastra \
  && npm install

CMD ["npm", "start"]
