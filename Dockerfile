FROM alpine:3.24.1

RUN apk add --no-cache ca-certificates \
    && addgroup -S dynatrace-exporter \
    && adduser -S -D -H -h /nonexistent -s /sbin/nologin -G dynatrace-exporter dynatrace-exporter

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/dynatrace-license-exporter /dynatrace-license-exporter

USER dynatrace-exporter:dynatrace-exporter
EXPOSE 9721
ENTRYPOINT ["/dynatrace-license-exporter"]
