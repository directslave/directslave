#!/bin/sh

echo "*** Building for Linux/x64 ..."
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -ldflags="-s -w" -o ../builds/directslave-linux-amd64 ../src/*.go
sudo install ../builds/directslave-linux-amd64 /usr/local/directslave/bin
echo "*** Done ***"
echo ""
