# EchoSphere Chat Server

EchoSphere is a lightweight Go + Vanilla JS stack that gives you a Discord-style workspace: email/password auth, persistent SQLite storage, multi-server + multi-channel text chat, and live message streaming over WebSockets.

## Current Features

- Email + password signup/login with bcrypt hashing
- Session cookies with secure random identifiers
- SQLite persistence for users, servers, channels, memberships, and chat history
- Multi-server / multi-channel text chat with channel unread indicators
- `/api/channels/{id}/messages` REST endpoint (GET history / POST new message)
- `/api/servers/{id}/members` endpoint for member lists, `/api/bootstrap` for initial state hydration
- `/ws` WebSocket endpoint delivers realtime channel events (subscribe/send)
- Create unlimited servers and text/voice channels; voice chat uses WebRTC per channel
- Modern single-page experience (no frameworks) with responsive layout and offline-friendly fallbacks

## Project Layout

```
.
├── main.go                 # HTTP server, auth, routing, REST controllers
├── storage.go              # Schema setup + data access helpers for users/servers/channels/messages
├── ws.go                  # WebSocket hub, client management, realtime broadcasting
├── go.mod / go.sum         # Module definition and dependencies
└── web
    ├── static
    │   ├── app.js          # Frontend logic, state management, WebSocket hookup, UI rendering
    │   └── styles.css      # Responsive, Discord-inspired styling
    └── templates
        ├── app.html        # Authenticated app shell, bootstraps initial data
        ├── login.html      # Login form
        └── signup.html     # Signup form
```

## Local Development (Windows, macOS, Linux)

1. Install Go 1.21+ (`go version`).
2. Fetch dependencies and verify the build:

   ```bash
   go mod tidy
   go build ./...
   ```

3. Run the server:

   ```bash
   go run .
   ```

4. Visit `http://localhost:8080/signup` to create an account, then you are redirected to the chat workspace. Channel history and memberships persist inside `./data/echosphere.db`.

## HTTP & Streaming APIs

| Endpoint | Method | Purpose |
| --- | --- | --- |
| `/api/bootstrap` | GET | Initial state (servers, default channel messages, members) after login |
| `/api/servers` | POST | Create a new server (owner becomes the creator) |
| `/api/servers/{id}` | GET | List channels inside a server |
| `/api/servers/{id}` | POST | Create a channel in the server (`{ name, kind }`, kind=`text`/`voice`) |
| `/api/servers/{id}/members` | GET | List members for the selected server |
| `/api/channels/{id}/messages` | GET | Fetch recent messages (`?limit=200`) |
| `/api/channels/{id}/messages` | POST | Send a chat message (JSON: `{ "content": "hello" }`) |
| `/ws` | WebSocket | Bidirectional channel for subscribing and sending chat events |

### Creating Servers & Channels

Use `POST /api/servers` with a JSON body like `{ "name": "Product Team" }` to spin up a workspace.
The creator is automatically added as an owner and a default `general` text channel is provisioned.

To add more rooms, `POST /api/servers/{serverId}` with `{ "name": "Design Sync", "kind": "voice" }` or "text" for a chat channel.
Each channel is addressable via `channelId` (needed for the WebSocket `subscribe`, `message`, and `voice:*` events).

All endpoints expect an authenticated session. WebSocket `message` events look like:

```json
{
  "id": 42,
  "channelId": 5,
  "authorEmail": "user@example.com",
  "authorDisplayName": "User",
  "content": "Hello world",
  "createdAt": "2025-10-05T19:20:30Z"
}
```

### WebSocket Events

| Event | Direction | Payload | Description |
| --- | --- | --- | --- |
| `subscribe` | client ? server | `{ channelId }` | Listen for channel messages in real time. |
| `message` | client ? server | `{ channelId, content }` | Post a text message (text channels only). |
| `voice:join` | client ? server | `{ channelId }` | Join a voice channel. Returns `voice:participants`. |
| `voice:leave` | client ? server | `{ channelId }` | Leave the voice channel. |
| `voice:participants` | server ? client | `{ channelId, participants: [], self: {} }` | Snapshot of peers currently in the voice room. |
| `voice:peer-joined` | server ? client | `{ channelId, peer: {} }` | Another participant joined; expect an SDP offer. |
| `voice:peer-left` | server ? client | `{ channelId, peer: {} }` | Participant disconnected; remove their stream. |
| `voice:signal` | bidirectional | `{ channelId, signal: { from, payload } }` | Forward WebRTC SDP/ICE payloads between peers. |

`voice:signal` payloads wrap either `{ kind: "sdp", description: RTCSessionDescription }` or `{ kind: "candidate", candidate: RTCIceCandidate }`.


| Event | Direction | Payload | Description |
| --- | --- | --- | --- |
| `voice:join` | client ? server | none | Request to join the shared voice room. Returns a `voice:participants` message with current peers. |
| `voice:leave` | client ? server | none | Leave the voice room and notify other peers. |
| `voice:participants` | server ? client | `{ participants: [], self: {} }` | Snapshot of current peers plus the caller's voice ID. |
| `voice:peer-joined` | server ? client | `{ peer: {} }` | Another participant joined; await their offer. |
| `voice:peer-left` | server ? client | `{ peer: {} }` | Participant disconnected; remove audio for that peer. |
| `voice:signal` | bidirectional | `{ signal: { from, payload } }` | Forward WebRTC SDP/ICE payloads between peers for negotiation. |

`voice:signal` payloads wrap either `{ kind: "sdp", description: RTCSessionDescription }` or `{ kind: "candidate", candidate: RTCIceCandidate }`.

## Linux Server Deployment (Ubuntu 22.04+)

The steps below show how to deploy on a fresh Ubuntu server using systemd. Adjust paths if you prefer a different layout.

### 1. Install base packages

```bash
sudo apt update
sudo apt install -y curl git ufw
```

### 2. Install Go from the official tarball

```bash
GO_VERSION="1.24.5"
curl -OL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
rm "go${GO_VERSION}.linux-amd64.tar.gz"
```

Append Go to your shell profile and reload:

```bash
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
```

### 3. Create an application user and directory

```bash
sudo useradd -m -s /bin/bash echosphere
sudo mkdir -p /opt/echosphere
sudo chown -R echosphere:echosphere /opt/echosphere
```

### 4. Deploy the source

Option A – build locally then upload:

```bash
GOOS=linux GOARCH=amd64 go build -o echosphere
scp echosphere -r web echosphere@YOUR_SERVER_IP:/opt/echosphere/
scp main.go storage.go ws.go go.mod go.sum echosphere@YOUR_SERVER_IP:/opt/echosphere/
```

Option B – build on the server:

```bash
ssh echosphere@YOUR_SERVER_IP
cd /opt/echosphere
git clone YOUR_REPO_URL .   # or copy files via scp/rsync
go build -o echosphere
```

Ensure the data directory exists and is writable:

```bash
mkdir -p /opt/echosphere/data
chown echosphere:echosphere /opt/echosphere/data
```

### 5. Configure systemd

Create `/etc/systemd/system/echosphere.service` with root privileges:

```ini
[Unit]
Description=EchoSphere Chat Server
After=network.target

[Service]
User=echosphere
Group=echosphere
WorkingDirectory=/opt/echosphere
ExecStart=/opt/echosphere/echosphere
Restart=on-failure
Environment=PORT=8080

[Install]
WantedBy=multi-user.target
```

Reload systemd and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now echosphere
sudo systemctl status echosphere
```

### 6. Open the firewall

```bash
sudo ufw allow OpenSSH
sudo ufw allow 8080/tcp
sudo ufw enable   # answer "y" when prompted
```

### 7. Add a reverse proxy (recommended)

Use Nginx, Caddy, or another proxy to terminate TLS and forward traffic to the Go service. Example Nginx block:

```nginx
server {
    listen 80;
    server_name chat.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

After verifying HTTP, request certificates with Let's Encrypt (`sudo certbot --nginx`).

### 8. Verify the deployment

- Tail logs: `sudo journalctl -u echosphere -f`
- Browse to `http://SERVER_IP:8080/signup`, create two users, pick different channels, and confirm messages stream live between browsers in real time.

## Roadmap

Upcoming milestones:

1. **Voice rooms (single room)** - introduce WebRTC signaling + an SFU backend to support live audio in the default room.
2. **Full workspace parity** - multi-room voice, screen sharing, richer presence, and polished moderation controls.

Contributions welcome - feel free to tackle the voice milestone or polish the new WebSocket client.

Happy chatting!
