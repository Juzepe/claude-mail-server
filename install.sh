#!/bin/bash
set -eo pipefail

# ============================================================
# Mail Server Installer
# Supports: Ubuntu 20.04, 22.04, 24.04 / Debian 11, 12
# ============================================================

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m' # No Color

# --- Helpers ---
info()    { echo -e "${CYAN}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}[OK]${NC} $1"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $1"; }
error()   { echo -e "${RED}[ERROR]${NC} $1" >&2; exit 1; }
step()    { echo -e "\n${BLUE}==>${NC} ${WHITE}$1${NC}"; }

# --- Check root ---
if [[ $EUID -ne 0 ]]; then
    error "This installer must be run as root. Try: sudo bash install.sh"
fi

# --- Check OS ---
if [[ ! -f /etc/os-release ]]; then
    error "Cannot detect OS. /etc/os-release not found."
fi
source /etc/os-release
if [[ "$ID" != "ubuntu" && "$ID" != "debian" ]]; then
    error "Unsupported OS: $ID. Only Ubuntu and Debian are supported."
fi
info "Detected OS: $PRETTY_NAME"

# --- Banner ---
echo -e "${BLUE}"
echo "  ╔══════════════════════════════════════════╗"
echo "  ║       Mail Server Installer v1.0         ║"
echo "  ║  Postfix + Dovecot + OpenDKIM + Web UI   ║"
echo "  ╚══════════════════════════════════════════╝"
echo -e "${NC}"

# --- Prompts ---
step "Configuration"

while true; do
    read -p "Enter your mail domain (e.g., example.com): " DOMAIN
    [[ -n "$DOMAIN" ]] && break
    warn "Domain cannot be empty."
done

while true; do
    read -p "Enter admin panel email (e.g., admin@${DOMAIN}): " ADMIN_EMAIL
    [[ -n "$ADMIN_EMAIL" ]] && break
    warn "Admin email cannot be empty."
done

while true; do
    read -sp "Enter admin panel password: " ADMIN_PASSWORD
    echo
    read -sp "Confirm admin panel password: " ADMIN_PASSWORD2
    echo
    if [[ "$ADMIN_PASSWORD" == "$ADMIN_PASSWORD2" ]]; then
        [[ ${#ADMIN_PASSWORD} -ge 8 ]] && break
        warn "Password must be at least 8 characters."
    else
        warn "Passwords do not match. Try again."
    fi
done

# Derive hostname
MAIL_HOSTNAME="mail.${DOMAIN}"

info "Domain:    $DOMAIN"
info "Hostname:  $MAIL_HOSTNAME"
info "Admin:     $ADMIN_EMAIL"
echo

read -p "Proceed with installation? [y/N]: " CONFIRM
[[ "$CONFIRM" =~ ^[Yy]$ ]] || error "Installation cancelled."

# --- Install packages ---
step "Installing packages"
export DEBIAN_FRONTEND=noninteractive

# Pre-configure postfix to avoid interactive prompt
echo "postfix postfix/mailname string ${MAIL_HOSTNAME}" | debconf-set-selections
echo "postfix postfix/main_mailer_type string 'Internet Site'" | debconf-set-selections

apt-get update -qq
apt-get install -y -q \
    postfix \
    postfix-pcre \
    dovecot-core \
    dovecot-imapd \
    dovecot-pop3d \
    dovecot-lmtpd \
    opendkim \
    opendkim-tools \
    certbot \
    git \
    make \
    curl \
    wget \
    apache2-utils \
    python3-certbot \
    ca-certificates \
    openssl \
    mailutils \
    2>/dev/null || true

success "Packages installed."

# --- Go installation ---
step "Setting up Go"

GO_MIN_VERSION="1.21"
INSTALLED_GO=""

if command -v go &>/dev/null; then
    INSTALLED_GO=$(go version | awk '{print $3}' | sed 's/go//')
    info "Found Go $INSTALLED_GO"
fi

need_go_install=false
if [[ -z "$INSTALLED_GO" ]]; then
    need_go_install=true
else
    # Compare versions
    IFS='.' read -ra V1 <<< "$INSTALLED_GO"
    IFS='.' read -ra V2 <<< "$GO_MIN_VERSION"
    if (( V1[0] < V2[0] || (V1[0] == V2[0] && V1[1] < V2[1]) )); then
        warn "Go $INSTALLED_GO is too old (need $GO_MIN_VERSION+). Will install newer."
        need_go_install=true
    fi
fi

if [[ "$need_go_install" == "true" ]]; then
    GO_VERSION="1.21.6"
    ARCH=$(dpkg --print-architecture)
    case "$ARCH" in
        amd64) GO_ARCH="amd64" ;;
        arm64) GO_ARCH="arm64" ;;
        *)     GO_ARCH="amd64" ;;
    esac
    GO_URL="https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    info "Downloading Go ${GO_VERSION}..."
    wget -q "$GO_URL" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    export PATH="/usr/local/go/bin:$PATH"
    # Persist in profile
    cat > /etc/profile.d/golang.sh << 'GOEOF'
export PATH="/usr/local/go/bin:$PATH"
GOEOF
    success "Go $(go version | awk '{print $3}') installed."
else
    success "Go $INSTALLED_GO is sufficient."
fi

export PATH="/usr/local/go/bin:$PATH"

# --- Create vmail user ---
step "Creating vmail user"
groupadd -g 5000 vmail 2>/dev/null || true
useradd -g vmail -u 5000 vmail -d /var/mail/vhosts -m -s /usr/sbin/nologin 2>/dev/null || true
success "vmail user ready (uid=5000, gid=5000)."

# --- Create directory structure ---
step "Creating directories"
mkdir -p /var/mail/vhosts/${DOMAIN}
mkdir -p /etc/mailserver
mkdir -p /var/lib/mailserver/certs
mkdir -p /opt/mailserver
chown -R vmail:vmail /var/mail/vhosts
chmod 750 /var/mail/vhosts
success "Directories created."

# --- Determine script directory (project root) ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
info "Project root: $SCRIPT_DIR"

# --- Copy project files ---
step "Installing project files"
if [[ "$SCRIPT_DIR" != "/opt/mailserver" ]]; then
    cp -r "$SCRIPT_DIR"/* /opt/mailserver/
fi
success "Project files copied to /opt/mailserver"

# --- Build Go web UI ---
step "Building web UI"
cd /opt/mailserver/web

export GOPATH=/root/go
export GOCACHE=/root/.cache/go-build

info "Downloading Go dependencies..."
go mod download

info "Compiling web UI..."
if ! go build -o /usr/local/bin/mailserver-web .; then
    error "Go build failed. Run: cd /opt/mailserver/web && go build -o /usr/local/bin/mailserver-web . for details."
fi
chmod +x /usr/local/bin/mailserver-web
success "Web UI binary built: /usr/local/bin/mailserver-web"

# --- Hash admin password ---
step "Generating admin password hash"
ADMIN_PASSWORD_HASH=$(/usr/local/bin/mailserver-web -hashpw "$ADMIN_PASSWORD" 2>/dev/null || \
    python3 -c "
import subprocess, sys
result = subprocess.run(['openssl', 'passwd', '-6', '$ADMIN_PASSWORD'], capture_output=True, text=True)
print(result.stdout.strip())
")

# Fallback: use htpasswd (bcrypt)
if [[ -z "$ADMIN_PASSWORD_HASH" ]]; then
    ADMIN_PASSWORD_HASH=$(htpasswd -nbB "" "$ADMIN_PASSWORD" | cut -d: -f2)
fi

if [[ -z "$ADMIN_PASSWORD_HASH" ]]; then
    error "Failed to hash admin password."
fi
success "Admin password hashed."

# --- Write config ---
step "Writing config"
cat > /etc/mailserver/config.env << EOF
DOMAIN=${DOMAIN}
HOSTNAME=${MAIL_HOSTNAME}
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD_HASH=${ADMIN_PASSWORD_HASH}
DATA_DIR=/var/lib/mailserver
MAIL_DIR=/var/mail/vhosts
DOVECOT_USERS_FILE=/etc/dovecot/users
POSTFIX_VMAILBOX_FILE=/etc/postfix/vmailbox
EOF
chmod 600 /etc/mailserver/config.env
success "Config written to /etc/mailserver/config.env"

# --- Configure Postfix ---
step "Configuring Postfix"

# Back up original
cp /etc/postfix/main.cf /etc/postfix/main.cf.backup 2>/dev/null || true

# Generate main.cf from template
sed \
    -e "s/{{DOMAIN}}/${DOMAIN}/g" \
    -e "s/{{MAIL_HOSTNAME}}/${MAIL_HOSTNAME}/g" \
    "$SCRIPT_DIR/configs/postfix-main.cf.template" \
    > /etc/postfix/main.cf

# Copy master.cf
cp "$SCRIPT_DIR/configs/postfix-master.cf" /etc/postfix/master.cf

# Create virtual mailbox index files
touch /etc/postfix/vmailbox
touch /etc/postfix/virtual
postmap /etc/postfix/vmailbox
postmap /etc/postfix/virtual

success "Postfix configured."

# --- Configure Dovecot ---
step "Configuring Dovecot"

# Back up
cp /etc/dovecot/dovecot.conf /etc/dovecot/dovecot.conf.backup 2>/dev/null || true

# Generate from template
sed \
    -e "s/{{DOMAIN}}/${DOMAIN}/g" \
    -e "s/{{MAIL_HOSTNAME}}/${MAIL_HOSTNAME}/g" \
    "$SCRIPT_DIR/configs/dovecot.conf.template" \
    > /etc/dovecot/dovecot.conf

# Create users file
touch /etc/dovecot/users
chmod 640 /etc/dovecot/users
chown root:dovecot /etc/dovecot/users

success "Dovecot configured."

# --- Configure OpenDKIM ---
step "Configuring OpenDKIM"

mkdir -p /etc/opendkim/keys/${DOMAIN}

# Generate DKIM key pair
opendkim-genkey -s mail -d "${DOMAIN}" -D /etc/opendkim/keys/${DOMAIN}
chown -R opendkim:opendkim /etc/opendkim/keys
chmod 700 /etc/opendkim/keys/${DOMAIN}
chmod 600 /etc/opendkim/keys/${DOMAIN}/mail.private

# Write KeyTable
cat > /etc/opendkim/KeyTable << EOF
mail._domainkey.${DOMAIN} ${DOMAIN}:mail:/etc/opendkim/keys/${DOMAIN}/mail.private
EOF

# Write SigningTable
cat > /etc/opendkim/SigningTable << EOF
*@${DOMAIN} mail._domainkey.${DOMAIN}
EOF

# Write TrustedHosts
cat > /etc/opendkim/TrustedHosts << EOF
127.0.0.1
localhost
${DOMAIN}
.${DOMAIN}
EOF

# Generate main opendkim.conf from template
sed \
    -e "s/{{DOMAIN}}/${DOMAIN}/g" \
    "$SCRIPT_DIR/configs/opendkim.conf.template" \
    > /etc/opendkim.conf

# Add opendkim to postfix group for socket access
usermod -aG postfix opendkim 2>/dev/null || true

success "OpenDKIM configured."

# --- SSL Certificate ---
step "Obtaining SSL certificate"

# Check if cert already exists
if [[ -f "/etc/letsencrypt/live/${MAIL_HOSTNAME}/fullchain.pem" ]]; then
    warn "Certificate for ${MAIL_HOSTNAME} already exists, skipping certbot."
else
    # Stop and disable any service that might occupy ports 80/443
    systemctl disable --now apache2 nginx 2>/dev/null || true

    certbot certonly \
        --standalone \
        -d "${MAIL_HOSTNAME}" \
        --agree-tos \
        --email "${ADMIN_EMAIL}" \
        --non-interactive \
        2>&1 | while IFS= read -r line; do info "$line"; done || {
            warn "certbot failed - ensure an A record for ${MAIL_HOSTNAME} points to this server."
        }
fi

# Cert is always requested for mail.domain
CERT_DIR="/etc/letsencrypt/live/${MAIL_HOSTNAME}"

if [[ -f "$CERT_DIR/fullchain.pem" ]]; then
    success "SSL certificate obtained: $CERT_DIR"
else
    warn "SSL certificate not found. You will need to configure TLS manually."
    CERT_DIR="/etc/letsencrypt/live/${DOMAIN}"
fi

# Update postfix/dovecot with correct cert path if needed
sed -i "s|{{CERT_DIR}}|${CERT_DIR}|g" /etc/postfix/main.cf 2>/dev/null || true
sed -i "s|{{CERT_DIR}}|${CERT_DIR}|g" /etc/dovecot/dovecot.conf 2>/dev/null || true

# --- Install systemd service ---
step "Installing systemd service"

sed \
    -e "s|{{DOMAIN}}|${DOMAIN}|g" \
    "$SCRIPT_DIR/systemd/mailserver-web.service" \
    > /etc/systemd/system/mailserver-web.service

systemctl daemon-reload
success "Systemd service installed."

# --- Configure firewall ---
step "Configuring firewall (ufw)"

if command -v ufw &>/dev/null; then
    ufw allow 25/tcp    comment "SMTP"       2>/dev/null || true
    ufw allow 465/tcp   comment "SMTPS"      2>/dev/null || true
    ufw allow 587/tcp   comment "Submission" 2>/dev/null || true
    ufw allow 80/tcp    comment "HTTP"       2>/dev/null || true
    ufw allow 443/tcp   comment "HTTPS"      2>/dev/null || true
    ufw allow 993/tcp   comment "IMAPS"      2>/dev/null || true
    ufw allow 995/tcp   comment "POP3S"      2>/dev/null || true
    ufw allow 143/tcp   comment "IMAP"       2>/dev/null || true
    ufw allow 110/tcp   comment "POP3"       2>/dev/null || true
    ufw allow 22/tcp    comment "SSH"        2>/dev/null || true
    success "Firewall rules added."
else
    warn "ufw not found. Please open ports 25, 465, 587, 80, 443, 993, 995, 143, 110 manually."
fi

# --- Enable and start services ---
step "Starting services"

systemctl enable postfix dovecot opendkim mailserver-web 2>/dev/null || true
systemctl restart opendkim  || warn "opendkim failed to start"
systemctl restart dovecot   || warn "dovecot failed to start"
systemctl restart postfix   || warn "postfix failed to start"
systemctl start  mailserver-web || warn "mailserver-web failed to start"

sleep 2

# Status check
for svc in postfix dovecot opendkim mailserver-web; do
    if systemctl is-active --quiet "$svc"; then
        success "$svc is running"
    else
        warn "$svc is NOT running - check: journalctl -u $svc"
    fi
done

# --- Extract DKIM public key ---
step "Extracting DKIM public key"
DKIM_KEY_FILE="/etc/opendkim/keys/${DOMAIN}/mail.txt"
if [[ -f "$DKIM_KEY_FILE" ]]; then
    DKIM_PUBLIC=$(grep -o '"[^"]*"' "$DKIM_KEY_FILE" | tr -d '"' | tr -d ' ' | tr -d '\n')
else
    DKIM_PUBLIC="<check /etc/opendkim/keys/${DOMAIN}/mail.txt>"
fi

# --- Add initial admin account to mail ---
step "Creating postmaster mailbox"
/usr/local/bin/mailserver-web -adduser "postmaster@${DOMAIN}" "$(openssl rand -base64 16)" 2>/dev/null || true

# --- Done! Print DNS records ---
echo
echo -e "${GREEN}╔══════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║              INSTALLATION COMPLETE!                             ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════════════════════════════╝${NC}"
echo
echo -e "${WHITE}Web UI:${NC} https://${MAIL_HOSTNAME}"
echo -e "${WHITE}Login:${NC}  ${ADMIN_EMAIL} / (your password)"
echo
echo -e "${YELLOW}=== DNS Records to configure ===${NC}"
echo -e "${WHITE}Type  Name                   Value${NC}"
echo -e "────  ─────────────────────  ─────────────────────────────────"
echo -e "MX    @                      ${MAIL_HOSTNAME} (priority 10)"
echo -e "A     mail                   $(curl -s4 ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')"
echo -e "TXT   @                      v=spf1 mx a:${MAIL_HOSTNAME} ~all"
echo -e "TXT   mail._domainkey        v=DKIM1; k=rsa; p=${DKIM_PUBLIC}"
echo -e "TXT   _dmarc                 v=DMARC1; p=quarantine; rua=mailto:postmaster@${DOMAIN}"
echo
echo -e "${YELLOW}=== SMTP/IMAP Credentials for apps ===${NC}"
echo -e "SMTP Host:        ${MAIL_HOSTNAME} (or ${DOMAIN})"
echo -e "SMTP Port:        587 (STARTTLS)"
echo -e "IMAP Host:        ${MAIL_HOSTNAME} (or ${DOMAIN})"
echo -e "IMAP Port:        993 (SSL/TLS)"
echo
echo -e "${CYAN}Manage email accounts at: https://${MAIL_HOSTNAME}${NC}"
echo -e "${CYAN}Config file: /etc/mailserver/config.env${NC}"
echo
echo -e "${YELLOW}NOTE:${NC} Set DNS records above BEFORE sending/receiving mail."
echo -e "${YELLOW}NOTE:${NC} SSL cert auto-renews via certbot's systemd timer."
echo
