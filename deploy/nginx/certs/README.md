# TLS certificate for the nginx example

Put the certificate chain and private key for `DOCKYARD_HOST` here (or point
`DOCKYARD_TLS_DIR` elsewhere). The file names must match
`DOCKYARD_TLS_CERT_FILE` / `DOCKYARD_TLS_KEY_FILE` (default
`fullchain.pem` / `privkey.pem`). Files in this directory other than this
README are git-ignored. See `docs/deployment.md`.
