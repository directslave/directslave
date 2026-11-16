#!/bin/sh

echo "*** Building for MacOS/x64 ..."
env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -v -ldflags="-s -w" -o ../builds/directslave-macos-amd64 ../src/*.go
sudo install ../builds/directslave-macos-amd64 /usr/local/directslave/bin
echo "*** Done ***"
echo ""
