FROM golang:1.23-bullseye AS build

WORKDIR /app

COPY ./app/go.mod ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY ./app .

RUN go build -ldflags="-linkmode external -extldflags -static" -tags netgo -o /app/main ./cmd/main.go

FROM alpine:3.19

COPY --from=build /app/main /main

CMD ["/main", "serve", "--http=0.0.0.0:8090"]