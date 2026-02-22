# youdl

A self-hosted video downloader with a controller/worker architecture. Submit a URL via the web UI, pick quality and trim options, and download the result. Powered by [yt-dlp](https://github.com/yt-dlp/yt-dlp) and [ffmpeg](https://ffmpeg.org/).

## Architecture

- **Controller** — web UI, job queue (SQLite), file storage, auth
- **Worker(s)** — poll the controller, run yt-dlp/ffmpeg, upload results

Workers can run on separate machines or in different regions. The controller distributes cookies and proxy lists to workers on startup.

## Requirements

- Go 1.22+
- `yt-dlp` in PATH (on workers)
- `ffmpeg` + `ffprobe` in PATH (on workers, for trimming)

## Quick start

```sh
# Build
make build

# Run controller
./bin/controller

# Run worker (on same or different machine)
YOUDL_CONTROLLER=http://localhost:8080 YOUDL_AUTH_TOKEN=<token> ./bin/worker
```

The auth token is printed by the controller on first run if not set.

## Configuration

### Controller

| Env var | Default | Description |
|---|---|---|
| `YOUDL_AUTH_TOKEN` | auto-generated | Shared secret between controller and workers |
| `YOUDL_LISTEN` | `:8080` | Listen address |
| `YOUDL_DB_PATH` | `youdl.db` | SQLite database path |
| `YOUDL_STORAGE_DIR` | `storage` | Directory for completed downloads |
| `YOUDL_JOB_TTL` | `30m` | How long to keep completed jobs |
| `YOUDL_PROXY_LIST` | — | Path to file with one proxy per line (distributed to workers) |
| `YOUDL_C_YOUTUBE` | — | Path to Netscape cookie file for YouTube |
| `YOUDL_C_REDDIT` | — | Path to Netscape cookie file for Reddit |
| `YOUDL_RATE_LIMIT` | `10` | Max URL submissions per IP per minute (0 = disabled) |
| `YOUDL_MAX_JOBS_PER_IP` | `5` | Max simultaneous active jobs per IP (0 = disabled) |
| `YOUDL_MAX_QUEUE_DEPTH` | `0` | Max total active jobs queue-wide (0 = disabled) |

### Worker

| Env var | Default | Description |
|---|---|---|
| `YOUDL_CONTROLLER` | `http://localhost:8080` | Controller base URL |
| `YOUDL_AUTH_TOKEN` | — | Shared secret (must match controller) |
| `YOUDL_WORKER_ID` | hostname | Unique worker identifier |
| `YOUDL_MAX_JOBS` | `2` | Concurrent downloads |
| `YOUDL_POLL_INTERVAL` | `5s` | How often to poll for new jobs |
| `YOUDL_THROTTLE` | `500ms` | Delay between yt-dlp requests |
| `YOUDL_MAX_DURATION` | `3600` | Max video duration in seconds (0 = unlimited) |
| `YOUDL_UPLOAD_LIMIT` | `0` | Upload bandwidth cap in bytes/s (0 = unlimited) |
| `YOUDL_NO_PROXY` | — | Set to any value to skip proxy fetch and use direct connection |

## Docker

```sh
# Controller
docker compose -f docker-compose.controller.yml up -d

# Worker
docker compose -f docker-compose.worker.yml up -d
```

## License

MIT
