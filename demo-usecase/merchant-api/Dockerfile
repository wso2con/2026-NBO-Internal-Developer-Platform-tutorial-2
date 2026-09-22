# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/merchant-api ./cmd/merchant-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/merchant-api /merchant-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/merchant-api"]
