#!/bin/sh

umask ${UMASK}

if [ "$1" = "version" ]; then
  ./openlist version
else
  if [ "$RUN_ARIA2" = "true" ]; then
    chown -R ${PUID}:${PGID} /opt/aria2/
    exec su-exec ${PUID}:${PGID} nohup aria2c \
      --enable-rpc \
      --rpc-allow-origin-all \
      --conf-path=/opt/aria2/.aria2/aria2.conf \
      >/dev/null 2>&1 &
  fi
if [ "$DATABASE_TYPE" != "sqlite3" ] && [ ! -f /opt/openlist/data/config.json ]; then
  mkdir -p /opt/openlist/data
  cat <<EOF > /opt/openlist/data/config.json
  {
    "database": {
      "type": "${DATABASE_TYPE}",
      "host": "${DATABASE_HOST}",
      "port": ${DATABASE_PORT},
      "user": "${DATABASE_USER}",
      "password": "${DATABASE_PASSWORD}",
      "dbname": "${DATABASE_NAME}"
    }
  }
  EOF
fi
  chown -R ${PUID}:${PGID} /opt/openlist/
  exec su-exec ${PUID}:${PGID} ./openlist server --no-prefix
fi