ARG ARCH="amd64"
ARG OS="linux"
FROM quay.io/prometheus/busybox-${OS}-${ARCH}:latest

ARG ARCH="amd64"
ARG OS="linux"
COPY .build/${OS}-${ARCH}/sentry_exporter   /bin/sentry_exporter

EXPOSE     9412
ENTRYPOINT [ "/bin/sentry_exporter" ]