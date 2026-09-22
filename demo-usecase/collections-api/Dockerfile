# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/collections-api ./cmd/collections-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/collections-api /collections-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/collections-api"]
