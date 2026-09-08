# Icinga Notifications | (c) 2023 Icinga GmbH | GPLv2+

FROM docker.io/library/golang AS build
ENV CGO_ENABLED=0
COPY . /src/icinga-notifications
WORKDIR /src/icinga-notifications

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test ./internal/daemon && make all

RUN make DESTDIR=/target install

FROM docker.io/library/alpine

COPY --from=build /target /

RUN apk add --no-cache ca-certificates tzdata && \
    apk upgrade --no-cache && \
    update-ca-certificates

# Support arbitrary OpenShift UIDs running with the root group.
RUN chown -R 0:0 /etc/icinga-notifications /usr/libexec/icinga-notifications /usr/share/icinga-notifications && \
    chmod -R g=u /etc/icinga-notifications /usr/libexec/icinga-notifications /usr/share/icinga-notifications

ARG username=notifications
RUN addgroup -g 1000 $username
RUN adduser -u 1000 -H -D -G $username $username
USER $username

EXPOSE 5680
CMD ["/usr/sbin/icinga-notifications"]
