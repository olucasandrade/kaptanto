FROM node:22-alpine

WORKDIR /app

COPY cursor-resume/api/package.json ./package.json
COPY cursor-resume/api/tsconfig.json ./tsconfig.json
RUN npm install

COPY cursor-resume/api/src ./src
COPY shared/src /shared/src

EXPOSE 4005

CMD ["npm", "run", "start"]
