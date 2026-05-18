FROM node:22-alpine

WORKDIR /app

COPY cursor-resume/web/package.json ./package.json
COPY cursor-resume/web/tsconfig.json ./tsconfig.json
COPY cursor-resume/web/vite.config.ts ./vite.config.ts
COPY cursor-resume/web/index.html ./index.html
RUN npm install

COPY cursor-resume/web/src ./src

EXPOSE 3000

CMD ["npm", "run", "start"]
