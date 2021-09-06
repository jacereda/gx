#!/bin/bash

export PROJECT_NAME="macro-guard"

set -e

PLATFORM="macOS"
GO="go"
TIME="time"
TIME_TOTAL="time"

if [[ -f /proc/version ]]; then
  if grep -q Linux /proc/version; then
    PLATFORM="lin"
    TIME="time --format=%es\n"
    TIME_TOTAL="time --format=total\t%es\n"
  fi
  if grep -q Microsoft /proc/version; then
    PLATFORM="win"
    CMAKE="cmake.exe"
    CLANG_FORMAT="clang-format.exe"
  fi
fi
GO="$TIME $GO"

case "$1" in
  # Desktop
  release)
    $GO run main.go
    ;;
esac
