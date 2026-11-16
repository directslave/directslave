#!/bin/sh

echo "*** Building for FreeBSD/x64 ..."
env CGO_ENABLED=0 go build -v -ldflags="-s -w" -o ../builds/directslave-freebsd-amd64 ../src/*.go
sudo install ../builds/directslave-freebsd-amd64 /usr/local/directslave/bin
echo "*** Done ***"
echo ""
