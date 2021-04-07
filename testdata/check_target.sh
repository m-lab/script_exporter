#!/bin/sh

if [[ -z $TARGET ]]; then
  echo "TARGET is undefined"
  exit 1
else
  echo "TARGET is ${TARGET}"
fi
