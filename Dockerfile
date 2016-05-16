FROM alpine:latest
MAINTAINER Alexey Diyan <alexey.diyan@gmail.com>

RUN set -x \
  && buildDeps='go git' \
  && apk add --update $buildDeps \
  && GOPATH=/tmp go get github.com/tgulacsi/prometheus_scollector \
  && mv /tmp/bin/prometheus_scollector /usr/local/bin/prometheus_scollector \
  && apk del $buildDeps \
  && rm -rf /tmp/*

ENTRYPOINT ["/usr/local/bin/prometheus_scollector"]
CMD ["--help"]

