# syntax=docker/dockerfile:1
# Builds the web app and serves it with Caddy, which also proxies to nodes.
FROM node:24-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
COPY web/package.json web/
RUN --mount=type=cache,target=/root/.npm npm ci --ignore-scripts
COPY web web
RUN npm -w web run build

FROM caddy:2-alpine
COPY deploy/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/web/dist /srv
