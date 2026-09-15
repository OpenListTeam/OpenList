# systemd socket activation

OpenList accepts these exact `FileDescriptorName` values:

| Name | Socket | Replaces |
| --- | --- | --- |
| `http` | TCP listening socket (`ListenStream`) | HTTP TCP listener |
| `https` | TCP listening socket (`ListenStream`) | HTTPS TCP listener |
| `quic` | UDP socket (`ListenDatagram`) | HTTP/3 (QUIC) listener |

A supplied socket takes precedence over the corresponding configured address and
port, including a port set to `-1`. A supplied `quic` socket also enables HTTP/3
when `enable_h3` is false or HTTPS TCP is disabled. HTTPS and QUIC still require
`scheme.cert_file` and `scheme.key_file`.

Without a matching named socket, that endpoint follows the existing configuration.
In particular, without a `quic` socket, HTTP/3 still requires `enable_h3` and a
configured HTTPS port, and uses the configured address and port. Unknown names
are ignored and their inherited descriptors are closed. Duplicate supported names
or an incorrect socket type cause startup to stop with an error in the log; they
do not silently fall back to binding another port. Unix socket, S3, FTP, and SFTP
activation is not implemented.

Use a separate socket unit for each name. For example, with a service named
`openlist.service`, create `openlist-http.socket`:

```ini
[Unit]
Description=OpenList HTTP socket

[Socket]
ListenStream=5244
FileDescriptorName=http
Service=openlist.service
Accept=no

[Install]
WantedBy=sockets.target
```

An optional `openlist-https.socket`:

```ini
[Unit]
Description=OpenList HTTPS socket

[Socket]
ListenStream=5245
FileDescriptorName=https
Service=openlist.service
Accept=no

[Install]
WantedBy=sockets.target
```

An optional `openlist-quic.socket`:

```ini
[Unit]
Description=OpenList HTTP/3 socket

[Socket]
ListenDatagram=5245
FileDescriptorName=quic
Service=openlist.service

[Install]
WantedBy=sockets.target
```

The service should run `openlist server` directly, or through a wrapper that uses
`exec`, so `LISTEN_PID` matches the OpenList process. Enable and start the desired
socket units while OpenList is stopped to avoid conflicts with its existing
listeners. Incoming traffic then starts the service. For example:

```sh
sudo systemctl stop openlist.service
sudo systemctl daemon-reload
sudo systemctl enable --now openlist-http.socket
```

When using features that construct URLs from configuration, such as the admin
CLI or forced HTTPS redirects, keep the configured HTTP/HTTPS ports aligned with
the socket units. QUIC's `Alt-Svc` advertisement uses the inherited UDP socket's
actual port.
