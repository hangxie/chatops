#!/bin/bash

set -euo pipefail

function build() {
    PKG_ARCH=$1

    case ${PKG_ARCH} in
        amd64)
            BIN_ARCH=amd64
            ;;
        arm64)
            BIN_ARCH=arm64
            ;;
        *)
            echo package for architecture ${PKG_ARCH} is not currently supported
            exit 0
            ;;
    esac

    DEB_VER=$(echo ${VERSION} | cut -f 1 -d \- | tr -d 'a-z')
    PKG_NAME=chatops
    DOCKER_NAME=deb-build-${BIN_ARCH}
    SOURCE_DIR=$(dirname $0)/../..

    # Launch build container
    docker ps -a | grep ${DOCKER_NAME} && docker rm -f ${DOCKER_NAME}
    docker run -di --rm --name ${DOCKER_NAME} debian:13-slim

    # CCI does not support volume mount, so use docker cp instead
    docker cp ${SOURCE_DIR}/package/deb ${DOCKER_NAME}:/tmp/
    docker exec ${DOCKER_NAME} mkdir -p /tmp/deb/usr/bin /tmp/deb/lib/systemd/system /tmp/deb/etc/chatops
    docker cp ${SOURCE_DIR}/package/systemd/chatops.service ${DOCKER_NAME}:/tmp/deb/lib/systemd/system/chatops.service
    docker cp ${SOURCE_DIR}/package/systemd/chatops.env ${DOCKER_NAME}:/tmp/deb/etc/chatops/chatops.env
    docker cp ${SOURCE_DIR}/package/systemd/config.yaml ${DOCKER_NAME}:/tmp/deb/etc/chatops/config.yaml
    docker cp ${SOURCE_DIR}/package/systemd/status.yaml ${DOCKER_NAME}:/tmp/deb/etc/chatops/status.yaml
    for BIN in ${BINARIES}; do
        docker cp ${SOURCE_DIR}/build/release/${BIN}-${VERSION}-linux-${BIN_ARCH}.gz ${DOCKER_NAME}:/tmp/${BIN}.gz
    done
    cat ${SOURCE_DIR}/package/deb/DEBIAN/control \
        | sed "s/^Version:.*/Version: ${DEB_VER}/; s/^Architecture:.*/Architecture: ${PKG_ARCH}/" > /tmp/control
    docker cp /tmp/control ${DOCKER_NAME}:/tmp/deb/DEBIAN/control

    # Build deb
    docker exec -t ${DOCKER_NAME} bash -c "
        set -euo pipefail;
        for BIN in ${BINARIES}; do
            gunzip /tmp/\${BIN}.gz;
            install -m 0755 /tmp/\${BIN} /tmp/deb/usr/bin/\${BIN};
        done;
        chmod 0644 /tmp/deb/lib/systemd/system/chatops.service;
        chmod 0640 /tmp/deb/etc/chatops/chatops.env /tmp/deb/etc/chatops/config.yaml /tmp/deb/etc/chatops/status.yaml;
        cd /tmp;
        dpkg-deb --build /tmp/deb;
    "
    docker cp ${DOCKER_NAME}:/tmp/deb.deb ${SOURCE_DIR}/build/release/${PKG_NAME}_${DEB_VER}_${PKG_ARCH}.deb

    # Clean up
    docker ps -a | grep ${DOCKER_NAME} && docker rm -f ${DOCKER_NAME}
}

build amd64
build arm64
