# Docker deployment

The container runs the Kasuri daemon, which serves the Web UI at `/ui`. It
is not a multi-user service. The daemon is loopback-only by
default. The container binds inside its network namespace so Docker can forward
the port, while every maintained host mapping is restricted to host loopback.

## Build and run

Create an ignored local configuration from the tracked template, fill all three
required model scopes, then build the image:

```bash
cp config.example.yaml config.yaml
docker build -t kasuri:6.0.0-dev .
```

The runtime image contains the static daemon binary plus Alpine's certificate
and timezone data. It intentionally contains no Go source, configuration, or
project files. Mount the configuration and only the project directories that
the daemon may inspect:

```bash
docker run --rm --name kasuri \
  -p 127.0.0.1:9090:9090 \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v "$(pwd)/projects:/app/projects:rw" \
  -e KASURI_CONFIG=/app/config.yaml \
  -e KASURI_BIND_ADDRESS=0.0.0.0:9090 \
  kasuri:6.0.0-dev
```

Use a project path inside the mounted `/app/projects` directory. A mounted
project receives local `.kasuri/` metadata and, after an explicit Apply,
the one reviewed source-file change. Keep `config.yaml` and its credentials
outside Git.

To publish beyond the host deliberately, change the port mapping yourself, for
example `-p 0.0.0.0:9090:9090`, and set `KASURI_BIND_ADDRESS` to a
non-loopback address if it is not already set by the container command. With a
non-loopback bind the daemon accepts requests addressed to loopback names and
IP addresses; list any other host names you use in `KASURI_ALLOWED_HOSTS`
(comma-separated, for example `KASURI_ALLOWED_HOSTS=devbox.lan`). Unlisted
names are rejected so a web page cannot reach the API by DNS rebinding. This
is still an unauthenticated API: provide network isolation and access controls
outside Kasuri. Do not use a public or untrusted network for this service.

## Compose

`docker compose up --build` uses the same configuration and project mounts.
The optional local LM Studio service is available with:

```bash
docker compose --profile with-llm up --build
```

Set each scope's `api_base_url` to `http://lm-studio:1234/v1` before using that
profile. Both published ports are loopback-only by default. The Compose file
uses the current Compose Specification and therefore has no legacy top-level
`version` declaration.

## Health and release verification

The image health check calls `GET /health` on the container's loopback address.
After starting a container, verify both liveness and its runtime contents:

```bash
curl --fail http://localhost:9090/health
docker inspect --format='{{.State.Health.Status}}' kasuri
docker image inspect kasuri:6.0.0-dev
```

Before release, build the image without cleanup commands and confirm the health
check is `healthy`. Do not claim container verification when Docker is
unavailable. The daemon API is documented in
[docs/api-contract.md](docs/api-contract.md); scoped provider configuration and
supported fields are in [CONFIG.md](CONFIG.md).
