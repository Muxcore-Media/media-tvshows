FROM golang:1.26-alpine AS builder
COPY core/ /build/core/
COPY contracts-media-admin/ /build/contracts-media-admin/
COPY media-tvshows/ /build/media-tvshows/
WORKDIR /build/media-tvshows
RUN go mod download && CGO_ENABLED=0 go build -o /media-tvshows ./cmd/module
FROM alpine:3.21
RUN adduser -D -h /data app
USER app
WORKDIR /app
COPY --from=builder /media-tvshows .
ENTRYPOINT ["./media-tvshows"]
