FROM golang:1.27-bookworm AS build

WORKDIR /app

COPY ./app/go.mod ./app/go.sum ./

RUN go mod download

COPY ./app .

RUN go build -ldflags="-linkmode external -extldflags -static" -tags netgo -o /app/main ./cmd/main.go

FROM alpine:3.24

WORKDIR /app

COPY --from=build /app/main ./main

# Country lookups for tunnel connections, done locally so IPs never leave the
# server. DB-IP's free "IP to Country Lite" (CC BY 4.0, https://db-ip.com) is
# published monthly; try this month, then last month. A failed download only
# leaves countries empty, it never fails the build.
RUN mkdir -p /app/geo && y=$(date +%Y) && m=$(expr $(date +%m) + 0) && \
    for try in 1 2; do \
      f=$(printf 'dbip-country-lite-%04d-%02d.mmdb.gz' "$y" "$m"); \
      if wget -q -O /tmp/geo.gz "https://download.db-ip.com/free/$f" && gunzip -c /tmp/geo.gz > /app/geo/country.mmdb; then break; fi; \
      rm -f /app/geo/country.mmdb; m=$((m - 1)); if [ "$m" -eq 0 ]; then m=12; y=$((y - 1)); fi; \
    done; \
    rm -f /tmp/geo.gz; [ -s /app/geo/country.mmdb ] || echo "warning: GeoIP database not downloaded; countries will be empty"

CMD ["./main", "serve", "--http=0.0.0.0:8090", "--dir=/app/pb_data"]
