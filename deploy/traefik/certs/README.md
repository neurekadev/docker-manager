# Own TLS certificate for the Traefik example (optional)

Only needed when you do not use Let's Encrypt: put the certificate chain and
private key for `DOCKER_MANAGER_HOST` here (or point `DOCKER_MANAGER_TLS_DIR` elsewhere)
and set `DOCKER_MANAGER_TLS_CERT_FILE` / `DOCKER_MANAGER_TLS_KEY_FILE` to their paths
below `/certs`. Files in this directory other than this README are
git-ignored. See `docs/internal/deployment.md`.
