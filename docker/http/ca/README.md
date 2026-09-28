# Extra CA certificates for engram-http

`docker-compose.http.yml` mounts this directory read-only at
`/usr/local/share/engram-ca` and adds it to `SSL_CERT_DIR`, so engram-http
trusts any PEM certificate placed here in addition to the system roots.

Use it for a private CA, such as the local Caddy CA created by the `tls`
profile of `docker-compose.cloud.yml`:

```bash
docker cp engram-cloud-caddy:/data/caddy/pki/authorities/local/root.crt \
  docker/http/ca/engram-cloud-local-ca.crt
chmod 644 docker/http/ca/engram-cloud-local-ca.crt  # container runs as uid 10001
docker compose -f docker-compose.http.yml up -d --force-recreate
```

If the Caddy data volume (`engram-cloud-caddy-data`) is ever recreated, Caddy
generates a NEW local CA: re-export `root.crt` as above and recreate
engram-http, or its cloud calls fail certificate verification.

Certificates (`*.crt`, `*.pem`) in this directory are git-ignored. A server
with a publicly trusted certificate (for example Let's Encrypt) needs nothing
here.
