FROM --platform=$BUILDPLATFORM registry-cn-hangzhou.ack.aliyuncs.com/dev/golang:1.22.3 as builder
WORKDIR /src
ARG TARGETARCH
ARG TARGETOS
RUN --mount=type=bind,target=. \
    export GOOS=$TARGETOS && \
    export GOARCH=$TARGETARCH && \
    export CGO_ENABLED=0 && \
    go build -o /out/csi-mount-proxy-server ./cmd/mount-proxy-server && \
    go build -o /out/csi-mount-proxy-client ./cmd/mount-proxy-client


# ossfs mount-proxy 

FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/alinux:3-update as oss
ARG TARGETPLATFORM
ARG OSSFS_VERSION=v1.91.3.ack.3

# install ossfs
RUN set -ex; \
    case "$TARGETPLATFORM" in \
        linux/amd64) OSSFS_ARCH="x86_64" ;; \
        linux/arm64) OSSFS_ARCH="aarch_64" ;; \
        *) echo "unknown platform"; exit 1 ;; \
    esac; \
    yum install -y https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/ossfs/ossfs_${OSSFS_VERSION}_centos8.0_${OSSFS_ARCH}.rpm; \
    yum install -y fuse-devel util-linux mailcap procps; \
    yum clean all

RUN cp /etc/mime.types /etc/csi-mime.types

COPY --link --from=builder /out/csi-mount-proxy* /usr/local/bin/
ENTRYPOINT ["csi-mount-proxy-server"]

FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/alinux:3-update as oss-1.88
ARG TARGETPLATFORM

# install ossfs
RUN set -ex; \
    case "$TARGETPLATFORM" in \
        linux/amd64) \
            yum install -y https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/pre/ossfs/ossfs_1.88.4_centos8.0_x86_64.rpm; \
            ;; \
        linux/arm64) \
            yum install -y \
                https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/multi-private/arm64-ossfs-v1.80.6/fuse-libs-2.9.2-11.el7.aarch64.rpm \
                https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/multi-private/arm64-ossfs-v1.80.6/fuse-2.9.2-11.el7.aarch64.rpm; \
            curl -sSL -o /usr/lib64/libfuse.so.2 https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/multi-private/arm64-ossfs-v1.80.6/libfuse.so.2; \
            curl -sSL -o /usr/local/bin/ossfs https://ack-csiplugin.oss-cn-hangzhou.aliyuncs.com/multi-private/arm64-ossfs-v1.80.6/ossfs-8u; \
            chmod +x /usr/local/bin/ossfs; \
            ;; \
        *) \
            echo "unknown platform"; \
            exit 1; \
            ;; \
    esac; \
    yum install -y util-linux mailcap procps && yum clean all; \
    mv /etc/mime.types /etc/csi-mime.types

COPY --link --from=builder /out/csi-mount-proxy* /usr/local/bin/
ENTRYPOINT ["csi-mount-proxy-server"]


# alinas mount-proxy 

FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/alinux:3-update as tini-downloader

ENV TINI_VERSION v0.19.0
ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini /tini
ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini.asc /tini.asc
RUN gpg --batch --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys 595E85A6B1B4779EA4DAAEC70B588DFF0527A9B7 \
    && gpg --batch --verify /tini.asc /tini
RUN chmod +x /tini

FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/alinux:3-update as alinas
# install tini
COPY --link --from=tini-downloader /tini /sbin/tini
# install alinas packages
RUN --mount=type=bind,target=/build,source=build/mount-proxy \
    yum install -y procps \
    && yum install -y /build/*.rpm \
    && yum clean all
RUN cp -r /etc/aliyun /etc/aliyun-defaults
COPY --link --from=builder /out/csi-mount-proxy* /usr/local/bin/
ENTRYPOINT [ "tini", "--", "csi-mount-proxy-server" ]

