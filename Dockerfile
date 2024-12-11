# Build
FROM golang:1.22.8-alpine3.20 AS builder

# RUN apk add --no-cache gcc musl-dev linux-headers git make
RUN apk add --no-cache build-base  linux-headers git bash ca-certificates  libstdc++

WORKDIR /n42
ADD . .
# feasible. Domestic users can cancel the comment on the following line when building images to use domestic proxies,
# ENV GOPROXY=https://goproxy.cn,direct 
ENV GO111MODULE="on"
RUN go mod tidy && go build  -o ./build/bin/n42 ./cmd/n42


FROM alpine:3.20
#libstdc++
RUN apk add --no-cache ca-certificates curl tzdata
# copy compiled artifacts from builder
COPY --from=builder /n42/build/bin/* /usr/local/bin/

# Setup user and group
#
# from the perspective of the container, uid=1000, gid=1000 is a sensible choice, but if caller creates a .env
# (example in repo root), these defaults will get overridden when make calls docker-compose
ARG UID=1000
ARG GID=1000
RUN adduser -D -u $UID -g $GID n42


ENV DATA /home/n42
# this 777 will be replaced by 700 at runtime (allows semi-arbitrary "--user" values)
RUN mkdir -p "$DATA" && chown -R n42:n42 "$DATA" && chmod 777 "$DATA"
VOLUME /home/n42

USER n42
WORKDIR /home/n42

RUN echo $UID

EXPOSE 20012 20013 20014 61015/udp 61016  6060
ENTRYPOINT ["n42"]
