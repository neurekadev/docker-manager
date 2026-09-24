# Own TLS certificate for the Traefik example (optional)

Only needed when you do not use Let's Encrypt: put the certificate chain and
private key for `DOCKYARD_HOST` here (or point `DOCKYARD_TLS_DIR` elsewhere)
and set `DOCKYARD_TLS_CERT_FILE` / `DOCKYARD_TLS_KEY_FILE` to their paths
below `/certs`. Files in this directory other than this README are
git-ignored. See `docs/deployment.md`.
