#!/usr/bin/env bash

set -euo pipefail

APP_NAME="http-monitor"
OUTPUT="${APP_NAME}.exe"
GOOS_TARGET="windows"
GOARCH_TARGET="amd64"

echo "==> 构建 ${GOOS_TARGET}/${GOARCH_TARGET} 可执行文件: ${OUTPUT}"

export GOOS="${GOOS_TARGET}"
export GOARCH="${GOARCH_TARGET}"

go build -o "${OUTPUT}"

echo "==> 构建完成: ${OUTPUT}"

PKG_DIR="dist-windows"
rm -rf "${PKG_DIR}"
mkdir -p "${PKG_DIR}"

cp "${OUTPUT}" "${PKG_DIR}/"

if [ -f "config.yaml" ]; then
  cp "config.yaml" "${PKG_DIR}/"
elif [ -f "config.yaml.example" ]; then
  cp "config.yaml.example" "${PKG_DIR}/config.yaml"
fi

if [ -f "README.md" ]; then
  cp "README.md" "${PKG_DIR}/"
fi

echo "==> 打包目录: ${PKG_DIR}"
echo "    - 将整个目录拷贝到 Windows 服务器，例如 C:\\${APP_NAME}"
echo "    - 在 Windows 上以管理员身份执行:"
echo "        ${APP_NAME}.exe -service install"
echo "        ${APP_NAME}.exe -service start"


