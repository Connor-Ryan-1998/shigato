FROM --platform=$BUILDPLATFORM node:24.22.2-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=secret,id=laptop_ca,target=/etc/ssl/certs/ca-certificates.crt \
    NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=laptop_ca,target=/etc/ssl/certs/ca-certificates.crt \
    go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/. /src/internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /shigoto ./cmd/shigoto

# Preserve the notices required when redistributing the compiled dependencies.
RUN CGO_ENABLED=0 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Dir}}{{end}}{{end}}' ./cmd/shigoto > /tmp/module-dirs \
    && sort -u -o /tmp/module-dirs /tmp/module-dirs \
    && cache="$(go env GOMODCACHE)" \
    && mkdir /licenses \
    && while IFS= read -r directory; do \
         [ -n "$directory" ] || continue; \
         destination="/licenses/${directory#"$cache"/}"; \
         mkdir -p "$destination"; \
         find "$directory" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'notice*' -o -iname 'copying*' -o -iname 'copyright*' \) \
           -exec cp '{}' "$destination/" \;; \
       done < /tmp/module-dirs \
    && cp /usr/local/go/LICENSE /licenses/GO-LICENSE

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /shigoto /shigoto
COPY --from=build /licenses /licenses
USER 65532:65532
ENTRYPOINT ["/shigoto"]
