# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY . .
# Constraint 5: nothing environment-specific is baked in. A Vite build inlines
# import.meta.env.* into the bundle, so API addresses arrive at CONTAINER START instead -
# see docker-entrypoint.sh and src/config.ts. The same image promotes between environments.
RUN npm run build

FROM nginx:alpine
COPY nginx.conf /etc/nginx/nginx.conf
COPY docker-entrypoint.sh /usr/local/bin/mopay-console-entrypoint.sh
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mopay-console-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
