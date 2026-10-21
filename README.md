# Track Foundry multiplayer backend

This is the standalone, in-memory backend for `../frontend/track-foundry-v11.html`. It owns room membership, host permissions, readiness, shared setup, countdown timestamps, and relaying of client-reported vehicle state. It does **not** simulate or validate vehicle physics server-side; this is a casual multiplayer prototype, not an anti-cheat service.

## Run locally

```bash
cd backend
go run .
```

The service listens on `0.0.0.0:$PORT`, defaulting to `0.0.0.0:8080`. Confirm it is ready with:

```bash
curl http://localhost:8080/health
```

Run the backend tests with `go test ./...`.

## Deploy on Render

Either use this directory as the repository root or set Render's **Root Directory** to `backend`.

- Environment: **Go**
- Build command: `go build -o track-foundry-server .`
- Start command: `./track-foundry-server`
- Environment variable: `ALLOWED_ORIGINS=https://sites.google.com,https://YOUR-PUBLISHED-SITE-ORIGIN`

Render supplies `PORT`; do not set it manually. `render.yaml` carries the same basic configuration for Blueprint deployments. After deployment, set this near the beginning of `frontend/track-foundry-v11.html`:

```js
const MULTIPLAYER_BACKEND_URL = 'https://YOUR-RENDER-SERVICE.onrender.com';
```

The frontend derives `wss://…/ws` from an HTTPS URL. For local development an `http://localhost:8080` URL derives `ws://…/ws`.

Browser origins contain only scheme and host, not a Google Sites page path. Add the actual published-page origin if it differs from `https://sites.google.com` (for example, a custom domain). The server also permits the documented `localhost` and `127.0.0.1` development origins. Keep `ALLOWED_ORIGINS` explicit; do not use a wildcard in production.

## Endpoints and protocol

- `GET /health` — readiness probe.
- `POST /v1/rooms` — creates a room (`name`, `config`) and returns a session token.
- `POST /v1/rooms/{code}/join` — joins or resumes a room (`name`, optional `token`).
- `GET /v1/rooms/{code}` — authenticated room snapshot with `Authorization: Bearer <token>`.
- `GET /ws` — live protocol endpoint.

WebSocket messages use `{ "version": 1, "type": "…", "payload": { … } }`. Client types are `create`, `join`, `auth`, `ready`, `config`, `start`, `state`, `finish`, `leave`, and `ping`. Server types are `session`, `room`, `race_start`, `state`, `room_closed`, `error`, `ping`, and `pong`.

The server generates the room code, participant id, and unpredictable resumption token. Tokens travel only in JSON message bodies or an HTTP authorization header, never in a URL. It limits message size, validates configuration/state values, limits vehicle reports to 20 per second, and enforces membership and host-only actions.

## Lifecycle and limitations

- A dropped participant remains visible as reconnecting for two minutes. Reusing its session token resumes the same participant without duplication.
- A host leaving deliberately closes the room. If the host fails to reconnect within two minutes, cleanup closes it.
- Empty rooms and rooms inactive for 30 minutes are removed. Rooms exist only in memory; a Render restart loses all rooms and clients must create a new one.
- The host syncs a complete `TF1.` track code plus race-affecting settings. Syncing setup resets readiness. Starting locks the configuration and schedules one server-issued timestamp five seconds ahead.
- Each browser simulates its own car and sends 10–20 state reports per second. Other clients smooth those snapshots. Finish order and timing are reported by clients and should be displayed as unverified results.
- Free Render services can take 30–60 seconds to wake. The frontend health-checks/retries for up to 90 seconds, shows progress, and leaves solo play available throughout.
