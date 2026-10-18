# Libre Publish in a container: the one binary (API, admin, SQLite, the
# default plugins, all built in) on a small image. Everything it keeps lives
# in /data: mount a named volume there, one per site. It refuses to start
# without one (EPHEMERAL_DATA=1 allows a trial): with no volume, or Docker's
# anonymous one, every deploy would start the site afresh. There's no VOLUME
# line for that reason: it would make Docker hand each container a new one.
#
#   docker build -t librepublish .
#   docker run -p 8080:80 -v librepublish-data:/data -e APP_URL=https://cms.example.com librepublish
#
# Settings are environment variables (README): APP_URL, SECRET_KEY,
# TRUST_PROXY=1 behind the host's proxy, PORT (80 here), LOG_FORMAT.
# The first admin: the setup screen at /cms/ on a fresh install, or
# `docker exec -it <container> librepublish user create --email … --name … --password -`.
# Health check: GET /up.

FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/librepublish ./cmd/librepublish

# Alpine for CA certificates (SMTP, webhooks, deploy hooks) and su-exec: the
# container starts as root only to hand /data to the app user, whatever owns
# the volume the host mounts, then runs as app (docker/librepublish).
FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec tzdata \
 && addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/librepublish /usr/local/libexec/librepublish
COPY docker/librepublish /usr/local/bin/librepublish
RUN chmod 0755 /usr/local/bin/librepublish
# Port 80: what Kamal-style hosts (kamal-proxy) send traffic and health
# checks to by default. A host that sets PORT moves it.
ENV DATA_DIR=/data LOG_FORMAT=json PORT=80
EXPOSE 80
ENTRYPOINT ["librepublish"]
CMD ["serve"]
