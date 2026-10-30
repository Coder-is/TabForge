#!/usr/bin/env bash
# Stamp binaries with the single release version source.
Version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' release.json)
if [[ -z ${Version} ]]; then
  echo "release.json: missing version" >&2
  exit 1
fi

BuildSourcePackage="github.com/Coder-is/TabForge/build"
BinaryPackage="github.com/Coder-is/TabForge"
BinaryName="tabforge"

BuildBinary()
{
  set -e
  TargetDir=bin/"${1}"
  mkdir -p "${TargetDir}"
  export GOOS=${1}
  export GOARCH=amd64 # the archive name below promises x86_64
  BuildTime=$(date -R)
  GitCommit=$(git rev-parse HEAD)
  VersionString="-X \"${BuildSourcePackage}.BuildTime=${BuildTime}\" -X \"${BuildSourcePackage}.Version=${Version}\" -X \"${BuildSourcePackage}.GitCommit=${GitCommit}\""

  go build -v -p 4 -o "${TargetDir}"/${BinaryName} -ldflags "${VersionString}" ${BinaryPackage}
  PackageDir=$(pwd)
  cd "${TargetDir}"
  tar zcvf "${PackageDir}"/${BinaryName}-${Version}-"${1}"-x86_64.tar.gz ${BinaryName}
  cd "${PackageDir}"
}


if [[ ${1} == "" ]]; then
  BuildBinary windows
  BuildBinary linux
  BuildBinary darwin
else
  BuildBinary "${1}"
fi

