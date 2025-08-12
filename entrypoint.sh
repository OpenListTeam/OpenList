#!/bin/sh

umask ${UMASK}

if [ "$1" = "version" ]; then
  ./openlist version
else
  # Define the target directory path for openlist service
  OPENLIST_DIR="/opt/service/start/openlist"
  if [ ! -d "$OPENLIST_DIR" ]; then
    cp -r /opt/service/stop/openlist "$OPENLIST_DIR" 2>/dev/null
  fi
  # Define the target directory path for aria2 service
  ARIA2_DIR="/opt/service/start/aria2"
  
  if [ "$RUN_ARIA2" = "true" ]; then
    # If aria2 should run and target directory doesn't exist, copy it
    if [ ! -d "$ARIA2_DIR" ]; then
      mkdir -p "$ARIA2_DIR"
      cp -r /opt/service/stop/aria2/* "$ARIA2_DIR" 2>/dev/null
    fi
  else
    # If aria2 should NOT run and target directory exists, remove it
    if [ -d "$ARIA2_DIR" ]; then
      rm -rf "$ARIA2_DIR"
    fi
  fi
  
  exec runsvdir /opt/service/start
fi
