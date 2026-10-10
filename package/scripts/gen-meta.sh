#!/bin/bash

set -euo pipefail

(cd ${BUILD_DIR}/release; \
    sha512sum *chatops* > checksum-sha512.txt)

# version file
echo ${VERSION} > ${BUILD_DIR}/VERSION

# changelog file: compare against the previous tag, or fall back to the
# full history for the first release (no earlier tag to describe).
if PREV_VERSION=$(git describe --abbrev=0 --tags ${VERSION}^ 2>/dev/null); then
    CHANGELOG_HEADER="Changes since [${PREV_VERSION}](https://github.com/hangxie/chatops/releases/tag/${PREV_VERSION}):"
    LOG_RANGE="${VERSION}...${PREV_VERSION}"
else
    CHANGELOG_HEADER="Initial release."
    LOG_RANGE="${VERSION}"
fi
(
    echo "${CHANGELOG_HEADER}"
    echo
    git log --pretty=format:"* %h %s" ${LOG_RANGE}
    echo
) > ${BUILD_DIR}/CHANGELOG

# license file
cp LICENSE ${BUILD_DIR}/release/LICENSE
