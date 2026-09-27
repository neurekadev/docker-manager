# TLS certificate for the nginx example

Put the certificate chain and private key for `DOCKER_MANAGER_HOST` here (or point
`DOCKER_MANAGER_TLS_DIR` elsewhere). The file names must match
`DOCKER_MANAGER_TLS_CERT_FILE` / `DOCKER_MANAGER_TLS_KEY_FILE` (default
`fullchain.pem` / `privkey.pem`). Files in this directory other than this
README are git-ignored. See `docs/internal/deployment.md`.
