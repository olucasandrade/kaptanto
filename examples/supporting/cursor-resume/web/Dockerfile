FROM node:22-alpine

WORKDIR /app

COPY supporting/cursor-resume/web/package.json ./package.json
COPY supporting/cursor-resume/web/tsconfig.json ./tsconfig.json
COPY supporting/cursor-resume/web/vite.config.ts ./vite.config.ts
COPY supporting/cursor-resume/web/index.html ./index.html
RUN npm install

COPY supporting/cursor-resume/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
