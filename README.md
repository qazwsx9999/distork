# EchoSphere Voice Collaboration UI

EchoSphere is a Discord-inspired collaboration surface featuring live voice rooms, screen sharing prompts, and activity streams. A Go backend provides account signup/login with session cookies, while the frontend delivers the immersive UI.

## Project Layout

```
.
├── main.go                 # Go HTTP server with auth and session handling
├── go.mod / go.sum         # Module definition and dependencies
└── web
    ├── static
    │   ├── app.js          # Frontend logic and UI rendering
    │   └── styles.css      # Styling for app + auth pages
    └── templates
        ├── app.html        # Main application surface (requires auth)
        ├── login.html      # Sign-in form with inline error feedback
        └── signup.html     # Account creation form
```

## Running Locally (Windows, macOS, Linux)

1. Ensure Go 1.21+ is installed (`go version`).
2. From the project root, download dependencies and build:

   ```bash
   go mod tidy
   go build ./...
   ```

3. Start the development server:

   ```bash
   go run main.go
   ```

4. Visit `http://localhost:8080/signup` to create your first account. Subsequent requests to `/` redirect you to the app UI once signed in.

Sessions are held in memory; restarting the server clears accounts and sessions.

## Ubuntu Server Deployment Guide

The following example assumes Ubuntu 22.04 LTS or newer.

### 1. Install system packages

```bash
sudo apt update
sudo apt install -y curl git ufw
```

### 2. Install Go (official tarball)

```bash
GO_VERSION="1.24.5"
curl -OL https://go.dev/dl/go${1.24.5}.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
rm go${GO_VERSION}.linux-amd64.tar.gz
```

Append Go to your shell profile (e.g. `~/.profile`):

```bash
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
```

### 3. Create a deploy user and directories

```bash
sudo useradd -m -s /bin/bash echosphere
sudo mkdir -p /opt/echosphere
sudo chown -R echosphere:echosphere /opt/echosphere
```

### 4. Build and copy the application

On your local machine:

```bash
GOOS=linux GOARCH=amd64 go build -o echosphere
scp echosphere web -r echosphere@YOUR_SERVER_IP:/opt/echosphere/
scp main.go go.mod go.sum echosphere@YOUR_SERVER_IP:/opt/echosphere/
```

(Alternatively clone the repository directly on the server and run `go build` there.)

### 5. Configure a systemd service

On the server, create `/etc/systemd/system/echosphere.service`:

```ini
[Unit]
Description=EchoSphere Voice Collaboration Server
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

### 6. (Optional) Configure UFW firewall

```bash
sudo ufw allow OpenSSH
sudo ufw allow 8080/tcp
sudo ufw enable
```

### 7. Reverse proxy (optional but recommended)

Use Nginx or Caddy in front of the Go server to terminate TLS and map a domain. Example Nginx server block snippet:

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Add Let\'s Encrypt TLS with `certbot --nginx` afterward.

---

Deployment tips:
- Because accounts live in memory, wire an external database (PostgreSQL, MySQL, or SQLite) for persistence before production use.
- Set `PORT` (and future secrets) via environment variables in the systemd unit or a dotenv loader.
- Run the Go binary behind a process supervisor (systemd as shown) for auto restarts and logging.
