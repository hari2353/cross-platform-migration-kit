# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS test

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN go test ./... \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

COPY docker/smoke.sh /smoke.sh
RUN chmod 0755 /smoke.sh

CMD ["/bin/sh", "/smoke.sh"]

FROM scratch AS release

COPY --from=test /out/migrate /migrate

ENTRYPOINT ["/migrate"]
