FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/bifroest ./cmd/bifroest

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S bifroest && adduser -S bifroest -G bifroest

COPY --from=build /out/bifroest /usr/local/bin/bifroest

RUN mkdir -p /data && chown bifroest:bifroest /data

USER bifroest
WORKDIR /data

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/bifroest"]
CMD ["-config", "/data/config.yaml"]
