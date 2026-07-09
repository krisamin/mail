# maild — Go 메일 서버 데몬 (IMAP/SMTP/submission/Admin API)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/maild ./cmd/maild

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 maild
USER maild
COPY --from=build /out/maild /usr/local/bin/maild
# IMAP / SMTP(MX) / submission / Admin API — 특권 포트 회피 (Service에서 매핑)
EXPOSE 1143 2525 2587 8080
ENTRYPOINT ["maild"]
