#!/bin/sh

echo "*** Building for Linux/x64 ..."
env CGO_ENABLED=0 GOOS=linux GOARCH=arm go build -v -ldflags="-s -w" -o ../builds/directslave-linux-arm ../src/*.go
sudo install ../builds/directslave-linux-arm /usr/local/directslave/bin
echo "*** Done ***"
echo ""
