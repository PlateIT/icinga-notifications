# Icinga Notifications | (c) 2023 Icinga GmbH | GPLv2+

FROM bit-base-images-docker-hosted.nexus.bit.admin.ch/bit/ca-bundle:latest AS certs
FROM docker-hub.nexus.bit.admin.ch/library/golang AS build

COPY --from=certs /certs/ /usr/local/share/ca-certificates/
RUN update-ca-certificates

ENV CGO_ENABLED=0
COPY . /src/icinga-notifications
WORKDIR /src/icinga-notifications

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make all

RUN make DESTDIR=/target install

FROM docker-hub.nexus.bit.admin.ch/library/alpine

COPY --from=certs /certs/ /usr/local/share/ca-certificates/
RUN cat /usr/local/share/ca-certificates/*.crt >> /etc/ssl/certs/ca-certificates.crt && \
    update-ca-certificates && \
    apk upgrade --no-cache

COPY --from=build /target /
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

RUN chown -R 0:0 /etc/icinga-notifications /usr/libexec/icinga-notifications /usr/share/icinga-notifications && \
    chmod -R g=u /etc/icinga-notifications /usr/libexec/icinga-notifications /usr/share/icinga-notifications

EXPOSE 5680
CMD ["/usr/sbin/icinga-notifications"]
