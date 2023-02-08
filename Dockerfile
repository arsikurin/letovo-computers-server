FROM golang:1.19-alpine AS base

STOPSIGNAL SIGINT

WORKDIR /base

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .
RUN go build -o server


FROM alpine:latest AS dev

STOPSIGNAL SIGINT

WORKDIR /app

COPY --from=base /base/server ./
COPY --from=base /base/.env ./

CMD ["./server", "-debug"]


FROM alpine:latest AS release

STOPSIGNAL SIGINT

WORKDIR /app

COPY --from=base /base/server ./
COPY --from=base /base/.env ./

CMD ["./server"]