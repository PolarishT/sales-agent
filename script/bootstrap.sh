#!/bin/bash
CURDIR=$(cd $(dirname $0); pwd)
BinaryName=sales-agent
echo "$CURDIR/bin/${BinaryName}"
exec $CURDIR/bin/${BinaryName}