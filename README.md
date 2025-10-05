# EchoSphere Chat Server

EchoSphere is a lightweight Go chat stack with session-based authentication, a persistent SQLite datastore, and a browser client that streams messages live over Server-Sent Events (SSE). Users can sign up, log in, and exchange messages in real time.

## Features

- Email + password signup/login backed by bcrypt hashing
- Cookie sessions with secure random identifiers
- SQLite persistence for users and chat history
- `/api/messages` REST endpoint (GET history, POST new message)
- `/events` SSE stream for broadcasting new chat messages instantly
- Single-page frontend (vanilla JS + CSS) packaged under `web/`

## Project Layout

```
.
├── main.go                 # HTTP server, routing, handlers, SSE wiring
├── storage.go              # Database schema + access helpers
├── events.go               # In-memory pub/sub broker for SSE clients
├── go.mod / go.sum         # Module definition and dependencies
└── web
    ├── static
    │   ├── app.js          # Frontend logic, SSE hookup, composer
    │   └── styles.css      # Chat UI styling
    └── templates
        ├── app.html        # Authenticated shell
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

4. Visit `http://localhost:8080/signup` to create an account. After signing in you are redirected to `http://localhost:8080/` where the chat UI loads and streams messages.

By default SQLite data files live under `./data/echosphere.db`. Delete that file to reset the app.

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
scp main.go storage.go events.go go.mod go.sum echosphere@YOUR_SERVER_IP:/opt/echosphere/
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
- Browse to `http://SERVER_IP:8080/signup`, create two users, and confirm messages stream live between sessions.

## Operational Notes

- **Persistence**: The bundled SQLite file (`data/echosphere.db`) keeps accounts and history. Back it up regularly or migrate to PostgreSQL/MySQL for multi-instance deployments.
- **Configuration**: Override defaults via environment variables (e.g., `PORT`, future `DATABASE_URL`). Add them to the systemd unit or an `/etc/default/echosphere` drop-in.
- **TLS**: Always terminate HTTPS in production—reverse proxy examples above assume plaintext between proxy and app.
- **Logs**: systemd captures stdout/stderr; use `journalctl` for inspection or forward logs to your stack of choice.

Happy chatting!
