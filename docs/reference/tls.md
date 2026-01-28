
# TLS/HTTPS Configuration Reference

**Version**: v1.0.0-beta.1

Complete reference for TLS/HTTPS configuration in Loom - securing gRPC and HTTP connections with manual certificates, Let's Encrypt, self-signed certificates, and mutual TLS (mTLS).


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [TLS Modes](#tls-modes)
- [Server Configuration](#server-configuration)
- [Client Configuration](#client-configuration)
- [Manual TLS Mode](#manual-tls-mode)
- [Let's Encrypt Mode](#lets-encrypt-mode)
- [Self-Signed Mode](#self-signed-mode)
- [Mutual TLS (mTLS)](#mutual-tls-mtls)
- [Certificate Management](#certificate-management)
- [TLS Manager](#tls-manager)
- [Certificate Rotation](#certificate-rotation)
- [Testing TLS Connections](#testing-tls-connections)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)
- [Security Considerations](#security-considerations)
- [Error Codes](#error-codes)
- [See Also](#see-also)


## Quick Reference

### TLS Modes Comparison

| Mode | Use Case | Certificate Source | Auto-Renewal | Complexity |
|------|----------|-------------------|--------------|------------|
| **manual** | Production with existing certs | User-provided files | No (manual) | Low |
| **letsencrypt** | Public production servers | Let's Encrypt ACME | Yes (auto) | Medium |
| **self-signed** | Development/testing | Auto-generated | No | Low |
| **none** (disabled) | Local development | N/A | N/A | None |

### Quick Start Commands

```bash
# Manual mode (production)
looms serve --config config.yaml
# config.yaml has tls.mode: manual, tls.manual.cert_file, tls.manual.key_file

# Let's Encrypt mode
looms serve --config letsencrypt-config.yaml
# config.yaml has tls.mode: letsencrypt, tls.letsencrypt.domains, etc.

# Self-signed mode (dev/test)
looms serve --config dev-config.yaml
# config.yaml has tls.mode: self-signed, tls.self_signed.hostnames

# Client connection (TUI)
loom --tls --server prod.example.com:60051
loom --tls --tls-insecure --server dev.example.com:60051  # Self-signed
loom --tls --tls-ca-file ca.crt --server internal.example.com:60051
```

### Configuration Parameters

**Server** (`looms.yaml`):
```yaml
server:
  tls:
    enabled: true
    mode: manual | letsencrypt | self-signed
    manual:
      cert_file: /path/to/cert.pem
      key_file: /path/to/key.pem
      ca_file: /path/to/ca.pem  # Optional (mTLS)
      require_client_cert: false  # Optional (mTLS)
    letsencrypt:
      domains: [example.com, www.example.com]
      email: admin@example.com
      # ... (see Let's Encrypt section)
    self_signed:
      hostnames: [localhost, dev.example.com]
      ip_addresses: [127.0.0.1, 192.168.1.100]
      # ... (see Self-Signed section)
```

**Client** (TUI flags):
```bash
--tls                       Enable TLS
--tls-insecure              Skip certificate verification
--tls-ca-file <path>        Custom CA certificate
--tls-server-name <name>    Override server name verification
```


## Overview

Loom supports **TLS/HTTPS** for securing gRPC and HTTP/SSE connections between clients and servers. TLS provides:

- **Encryption**: Protects data in transit from eavesdropping
- **Authentication**: Verifies server identity (and optionally client identity with mTLS)
- **Integrity**: Prevents tampering with messages

**Supported Protocols**:
- gRPC over TLS (port 60051)
- HTTP/REST+SSE over HTTPS (port 8080)

**TLS Versions**:
- Minimum: TLS 1.2
- Recommended: TLS 1.3
- TLS 1.0/1.1: Not supported (deprecated)

**Implementation**:
- Server: `pkg/server/tls/manager.go` (TLS manager)
- Client: `pkg/tui/client/client.go` (TLS configuration)
- Available Since: v0.8.0


## Prerequisites

### System Requirements

- **Operating System**: Linux, macOS, Windows (with OpenSSL)
- **OpenSSL**: 1.1.1+ (for TLS 1.3 support)
- **Network**: Firewall must allow TCP on gRPC and HTTP ports

### Port Requirements

| Service | Protocol | Default Port | TLS Required? |
|---------|----------|--------------|---------------|
| gRPC | gRPC/HTTP2 | 60051 | Optional |
| HTTP/REST+SSE | HTTP/1.1 | 8080 | Optional |
| ACME Challenge | HTTP | 80 | Yes (Let's Encrypt mode only) |

**Firewall Rules** (Let's Encrypt mode):
```bash
# Allow ACME HTTP-01 challenge
sudo ufw allow 80/tcp

# Allow gRPC/HTTPS
sudo ufw allow 60051/tcp
sudo ufw allow 8080/tcp
```


### Certificate Requirements

**Valid Certificate Must Have**:
- Subject Alternative Name (SAN) matching server hostname
- Valid date range (not expired)
- Trusted CA signature (or custom CA trusted by client)
- Private key (for server certificates)

**Check Certificate**:
```bash
# View certificate details
openssl x509 -in cert.pem -text -noout

# Check expiration
openssl x509 -in cert.pem -noout -enddate

# Verify certificate chain
openssl verify -CAfile ca.pem cert.pem
```


## TLS Modes

### Mode: manual

**Use Case**: Production environments with existing certificates from a CA (commercial, internal, or self-managed).

**Pros**:
- ✅ Full control over certificates
- ✅ Works with any CA (commercial, internal PKI)
- ✅ No automatic network calls (isolated environments)
- ✅ Simple configuration

**Cons**:
- ❌ Manual certificate renewal required
- ❌ Must monitor expiration dates
- ❌ Initial certificate acquisition outside Loom

**Required Files**:
- `cert.pem` - Server certificate (PEM format)
- `key.pem` - Private key (PEM format, unencrypted)
- `ca.pem` - CA certificate (optional, for mTLS)


### Mode: letsencrypt

**Use Case**: Public production servers with internet-accessible domains.

**Pros**:
- ✅ Free certificates from Let's Encrypt
- ✅ Automatic renewal (no expiration issues)
- ✅ Trusted by all major browsers/clients
- ✅ Multi-domain support (SAN certificates)

**Cons**:
- ❌ Requires public DNS (domain must resolve)
- ❌ Requires port 80 open (HTTP-01 challenge)
- ❌ Rate limits (50 certificates per domain per week)
- ❌ Not suitable for internal/private networks

**Requirements**:
- Public IP address
- Domain name with DNS A/AAAA record pointing to server
- Port 80 accessible (HTTP-01 challenge)
- Accept Let's Encrypt Terms of Service


### Mode: self-signed

**Use Case**: Development, testing, internal environments without CA infrastructure.

**Pros**:
- ✅ Instant setup (no external dependencies)
- ✅ Works offline/air-gapped environments
- ✅ No DNS requirements
- ✅ Free

**Cons**:
- ❌ Not trusted by default (clients must skip verification or trust CA)
- ❌ Security warnings in browsers
- ❌ Not suitable for production
- ❌ Manual client configuration required

**Generated Automatically**: Loom generates certificate and private key on startup.


## Server Configuration

### Configuration File (`looms.yaml`)

**Location**: `$LOOM_DATA_DIR/looms.yaml` or specified with `--config`

**Complete Example**:
```yaml
server:
  host: 0.0.0.0
  port: 60051
  http_port: 8080
  enable_reflection: true

  tls:
    enabled: true
    mode: manual  # or: letsencrypt, self-signed

    # Manual mode configuration
    manual:
      cert_file: /etc/loom/certs/server.crt
      key_file: /etc/loom/certs/server.key
      ca_file: /etc/loom/certs/ca.crt  # Optional (mTLS)
      require_client_cert: false       # Optional (mTLS)

    # Let's Encrypt configuration
    letsencrypt:
      domains:
        - loom.example.com
        - loom-api.example.com
      email: admin@example.com
      acme_directory_url: https://acme-v02.api.letsencrypt.org/directory
      http_challenge_port: 80
      cache_dir: /var/loom/letsencrypt
      auto_renew: true
      renew_before_days: 30
      accept_tos: true

    # Self-signed configuration
    self_signed:
      hostnames:
        - localhost
        - dev.example.com
        - loom-dev
      ip_addresses:
        - 127.0.0.1
        - 192.168.1.100
      validity_days: 365
      organization: My Organization

agents:
  # ... agent configuration
```


### Enable TLS

**Minimal Configuration** (manual mode):
```yaml
server:
  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /path/to/cert.pem
      key_file: /path/to/key.pem
```

**Start Server**:
```bash
looms serve --config looms.yaml
```

**Expected Output**:
```
INFO  TLS enabled  mode=manual
INFO    Manual TLS configuration  cert=/path/to/cert.pem key=/path/to/key.pem
INFO    Certificate issuer  issuer=CN=My CA
INFO    Certificate domains  domains=[loom.example.com]
INFO    Certificate expiry  not_after=2026-12-11T10:00:00Z days_until_expiry=365
INFO  gRPC server TLS credentials applied
INFO  🚀 Loom server listening  grpc_addr=:60051 http_addr=:8080
```


### Disable TLS

**Configuration**:
```yaml
server:
  tls:
    enabled: false
```

**Or omit TLS section entirely** (defaults to disabled).

**Use Case**: Local development, testing, or when TLS is handled by reverse proxy (nginx, Traefik).


## Client Configuration

### TUI Client (`loom` command)

**TLS Flags**:
```bash
--tls                       # Enable TLS connection
--tls-insecure              # Skip certificate verification (self-signed certs)
--tls-ca-file <path>        # Path to custom CA certificate
--tls-server-name <name>    # Override server name for verification
```


### Connect to TLS Server

**Production Server** (valid certificate from trusted CA):
```bash
loom --tls --server prod.example.com:60051
```

**Self-Signed Certificate** (skip verification):
```bash
loom --tls --tls-insecure --server dev.example.com:60051
```

⚠️ **Warning**: `--tls-insecure` disables certificate verification. Only use for development/testing!


### Connect with Custom CA

**Scenario**: Server uses certificate from internal CA not in system trust store.

**Steps**:
1. Obtain CA certificate (`ca.crt`)
2. Use `--tls-ca-file` flag

**Example**:
```bash
loom --tls \
     --tls-ca-file /etc/loom/ca.crt \
     --server internal.example.com:60051
```

**How it works**: Client loads custom CA certificate and appends to system root CAs for verification.


### Override Server Name

**Scenario**: Testing with IP address or DNS name doesn't match certificate CN/SAN.

**Example**:
```bash
# Connect to 192.168.1.100 but certificate is for dev.example.com
loom --tls \
     --tls-server-name dev.example.com \
     --server 192.168.1.100:60051
```

**Use Case**: Testing, port forwarding, SSH tunnels.


### Programmatic Client (Go)

```go
import (
    "github.com/teradata-labs/loom/pkg/tui/client"
)

// TLS client
c, err := client.NewClient(client.Config{
    ServerAddr:    "prod.example.com:60051",
    TLSEnabled:    true,
    TLSInsecure:   false,  // Verify certificates
    TLSCAFile:     "",     // Use system CAs
    TLSServerName: "",     // Use server address hostname
})
if err != nil {
    log.Fatalf("Failed to connect: %v", err)
}
defer c.Close()

// Self-signed (dev)
c, err := client.NewClient(client.Config{
    ServerAddr:  "dev.example.com:60051",
    TLSEnabled:  true,
    TLSInsecure: true,  // Skip verification
})

// Custom CA
c, err := client.NewClient(client.Config{
    ServerAddr: "internal.example.com:60051",
    TLSEnabled: true,
    TLSCAFile:  "/etc/loom/ca.crt",
})
```


## Manual TLS Mode

### Configuration

```yaml
server:
  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /etc/loom/certs/server.crt
      key_file: /etc/loom/certs/server.key
      ca_file: /etc/loom/certs/ca.crt  # Optional (mTLS only)
      require_client_cert: false       # Optional (mTLS only)
```

### Parameters

#### cert_file

**Type**: `string` (file path)
**Required**: Yes
**Format**: PEM-encoded certificate

**Description**: Path to server certificate file.

**Certificate must contain**:
- Server certificate
- Intermediate certificates (if any)
- Root CA certificate (optional)

**Certificate chain order** (in file):
```
-----BEGIN CERTIFICATE-----
[Server Certificate]
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
[Intermediate CA Certificate 1]
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
[Intermediate CA Certificate 2]
-----END CERTIFICATE-----
```

**Generate Certificate** (example):
```bash
# Generate private key
openssl genrsa -out server.key 2048

# Create certificate signing request (CSR)
openssl req -new -key server.key -out server.csr \
    -subj "/CN=loom.example.com/O=My Organization"

# Sign with CA (or submit CSR to CA)
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key \
    -CAcreateserial -out server.crt -days 365 \
    -extensions v3_req -extfile openssl.cnf
```


#### key_file

**Type**: `string` (file path)
**Required**: Yes
**Format**: PEM-encoded RSA/EC private key (unencrypted)

**Description**: Path to private key file corresponding to certificate.

**Security**:
- ⚠️ Must be unencrypted (no passphrase)
- ⚠️ Protect with file permissions: `chmod 600 server.key`
- ⚠️ Owned by Loom server user

**Check Key Matches Certificate**:
```bash
# Compare modulus (should match)
openssl x509 -noout -modulus -in server.crt | openssl md5
openssl rsa -noout -modulus -in server.key | openssl md5
```


#### ca_file (Optional)

**Type**: `string` (file path)
**Required**: No (mTLS only)
**Format**: PEM-encoded CA certificate

**Description**: Path to CA certificate for validating client certificates (mutual TLS).

**Use Case**: mTLS - server verifies client certificates.

**See**: [Mutual TLS (mTLS)](#mutual-tls-mtls) section.


#### require_client_cert (Optional)

**Type**: `bool`
**Default**: `false`
**Required**: No

**Description**: Require client certificates (mutual TLS).

**Behavior**:
- `false`: Client certificates optional (server-only TLS)
- `true`: Client certificates required (mTLS), connections without client cert rejected

**See**: [Mutual TLS (mTLS)](#mutual-tls-mtls) section.


### Example Configuration

**Production Server**:
```yaml
server:
  host: 0.0.0.0
  port: 60051
  http_port: 8080

  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /etc/loom/certs/loom-prod.crt
      key_file: /etc/loom/certs/loom-prod.key
```

**Start Server**:
```bash
looms serve --config prod-config.yaml
```

**Client Connection**:
```bash
# If certificate from trusted CA
loom --tls --server loom.example.com:60051

# If self-signed or internal CA
loom --tls --tls-ca-file /etc/loom/certs/ca.crt --server loom.example.com:60051
```


### Certificate Renewal

**Manual mode does NOT auto-renew**. You must:

1. **Monitor expiration**:
   ```bash
   openssl x509 -in /etc/loom/certs/server.crt -noout -enddate
   ```

2. **Obtain new certificate** (before expiration):
   - Request from CA
   - Generate CSR
   - Receive new certificate

3. **Replace files**:
   ```bash
   cp new-server.crt /etc/loom/certs/server.crt
   cp new-server.key /etc/loom/certs/server.key
   ```

4. **Restart server**:
   ```bash
   sudo systemctl restart loom
   ```

**Recommendation**: Set up certificate monitoring (30 days before expiration alert).


## Let's Encrypt Mode

### Configuration

```yaml
server:
  tls:
    enabled: true
    mode: letsencrypt
    letsencrypt:
      domains:
        - loom.example.com
        - loom-api.example.com
      email: admin@example.com
      acme_directory_url: https://acme-v02.api.letsencrypt.org/directory
      http_challenge_port: 80
      cache_dir: /var/loom/letsencrypt
      auto_renew: true
      renew_before_days: 30
      accept_tos: true
```


### Parameters

#### domains

**Type**: `[]string` (array of domain names)
**Required**: Yes

**Description**: List of domains to include in certificate (Subject Alternative Names).

**Requirements**:
- Each domain must resolve to server's public IP (DNS A/AAAA record)
- Each domain must be internet-accessible
- Maximum: 100 domains per certificate

**Example**:
```yaml
domains:
  - loom.example.com
  - loom-api.example.com
  - loom-grpc.example.com
```

**Verification**:
```bash
# Check DNS resolution
dig +short loom.example.com
# Should return server IP

# Check HTTP accessibility
curl http://loom.example.com/.well-known/acme-challenge/test
```


#### email

**Type**: `string` (email address)
**Required**: Yes

**Description**: Email address for Let's Encrypt account (renewal notices, security alerts).

**Purpose**:
- Certificate expiration reminders (if auto-renewal fails)
- Security notifications
- Account recovery

**Example**:
```yaml
email: admin@example.com
```


#### acme_directory_url

**Type**: `string` (URL)
**Default**: `https://acme-v02.api.letsencrypt.org/directory`
**Required**: No

**Description**: ACME directory URL for certificate authority.

**Values**:
- Production: `https://acme-v02.api.letsencrypt.org/directory`
- Staging: `https://acme-staging-v02.api.letsencrypt.org/directory`

**Staging Use Case**: Testing (avoids rate limits, issues test certificates not trusted by browsers).

**Example** (staging for testing):
```yaml
acme_directory_url: https://acme-staging-v02.api.letsencrypt.org/directory
```

**⚠️ Warning**: Staging certificates are NOT trusted. Use only for testing.


#### http_challenge_port

**Type**: `int`
**Default**: `80`
**Required**: No

**Description**: Port for HTTP-01 ACME challenge server.

**Requirements**:
- Must be accessible from internet
- Must be port 80 (ACME spec requirement)

**Firewall**:
```bash
sudo ufw allow 80/tcp
```

**How it works**:
1. Let's Encrypt sends HTTP request to `http://<domain>/.well-known/acme-challenge/<token>`
2. Loom responds with challenge response
3. Let's Encrypt verifies and issues certificate


#### cache_dir

**Type**: `string` (directory path)
**Default**: `/var/loom/letsencrypt`
**Required**: No

**Description**: Directory for storing certificates, private keys, and ACME account data.

**Contents**:
- `<domain>.crt` - Certificate
- `<domain>.key` - Private key
- `<domain>.json` - ACME account data

**Permissions**:
```bash
sudo mkdir -p /var/loom/letsencrypt
sudo chmod 700 /var/loom/letsencrypt
sudo chown loom:loom /var/loom/letsencrypt
```


#### auto_renew

**Type**: `bool`
**Default**: `true`
**Required**: No

**Description**: Enable automatic certificate renewal.

**Behavior**:
- `true`: Loom checks daily for expiring certificates and renews automatically
- `false`: Manual renewal required (not recommended)

**Renewal Trigger**: Certificate expires in ≤ `renew_before_days` days.


#### renew_before_days

**Type**: `int32`
**Default**: `30`
**Range**: `1` - `89`
**Required**: No

**Description**: Days before expiration to trigger renewal.

**Recommendation**: 30 days (allows multiple retry attempts if renewal fails).

**Example**:
- Certificate expires: 2025-12-11
- Renew before: 30 days
- Renewal triggers: 2025-11-11


#### accept_tos

**Type**: `bool`
**Default**: `false`
**Required**: Yes (must be `true` to use Let's Encrypt)

**Description**: Accept Let's Encrypt Terms of Service.

**Terms**: https://letsencrypt.org/repository/

**Example**:
```yaml
accept_tos: true
```

**⚠️ Warning**: Server will not start if `accept_tos: false` in Let's Encrypt mode.


### Example Configuration

**Production Server**:
```yaml
server:
  host: 0.0.0.0
  port: 60051
  http_port: 8080

  tls:
    enabled: true
    mode: letsencrypt
    letsencrypt:
      domains:
        - loom.example.com
      email: admin@example.com
      accept_tos: true
```

**Start Server**:
```bash
# Ensure port 80 accessible
sudo ufw allow 80/tcp

# Start server (runs as non-root with port binding capability)
looms serve --config prod-config.yaml
```

**Expected Output**:
```
INFO  TLS enabled  mode=letsencrypt
INFO    Let's Encrypt configuration  domains=[loom.example.com] email=admin@example.com
INFO    Requesting certificate from Let's Encrypt...
INFO    ACME challenge started  domain=loom.example.com type=http-01
INFO    Certificate issued successfully
INFO    Certificate issuer  issuer=CN=R3,O=Let's Encrypt,C=US
INFO    Certificate domains  domains=[loom.example.com]
INFO    Certificate expiry  not_after=2025-03-11T10:00:00Z days_until_expiry=90
INFO  gRPC server TLS credentials applied
INFO  🚀 Loom server listening  grpc_addr=:60051 http_addr=:8080
```

**Client Connection**:
```bash
# Let's Encrypt certificates trusted by default
loom --tls --server loom.example.com:60051
```


### Rate Limits

**Let's Encrypt Rate Limits** (as of 2025):
- **Certificates per Registered Domain**: 50 per week
- **Duplicate Certificates**: 5 per week
- **Failed Validations**: 5 per account per hostname per hour
- **New Accounts**: 10 per IP per 3 hours

**Recommendation**: Use staging environment for testing to avoid hitting rate limits.

**Check Rate Limits**: https://letsencrypt.org/docs/rate-limits/


### Troubleshooting Let's Encrypt

**Issue**: Certificate request fails

**Check**:
```bash
# 1. DNS resolution
dig +short loom.example.com
# Should return server public IP

# 2. HTTP accessibility
curl http://loom.example.com/.well-known/acme-challenge/test
# Should connect (404 is OK, connection refused is not)

# 3. Firewall
sudo ufw status | grep 80
# Should show: 80/tcp ALLOW

# 4. Port binding (requires capability or root)
sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/looms
```


## Self-Signed Mode

### Configuration

```yaml
server:
  tls:
    enabled: true
    mode: self-signed
    self_signed:
      hostnames:
        - localhost
        - dev.example.com
        - loom-dev.local
      ip_addresses:
        - 127.0.0.1
        - 192.168.1.100
        - 10.0.0.50
      validity_days: 365
      organization: Development Team
```


### Parameters

#### hostnames

**Type**: `[]string` (array of hostnames)
**Required**: Yes (at least one hostname or IP)

**Description**: Hostnames to include in certificate Subject Alternative Names (SANs).

**Example**:
```yaml
hostnames:
  - localhost
  - dev.example.com
  - loom-dev.local
```

**Certificate will be valid for**: Connections to any listed hostname.


#### ip_addresses

**Type**: `[]string` (array of IP addresses)
**Required**: No

**Description**: IP addresses to include in certificate SANs.

**Example**:
```yaml
ip_addresses:
  - 127.0.0.1
  - 192.168.1.100
```

**Use Case**: Direct IP connections (testing, internal networks without DNS).


#### validity_days

**Type**: `int32`
**Default**: `365`
**Range**: `1` - `3650` (10 years)
**Required**: No

**Description**: Certificate validity period in days.

**Recommendation**:
- Development: 365 days (1 year)
- Long-term testing: 730 days (2 years)

**Example**:
```yaml
validity_days: 365
```


#### organization

**Type**: `string`
**Default**: `Loom Self-Signed`
**Required**: No

**Description**: Organization name in certificate subject.

**Example**:
```yaml
organization: My Development Team
```

**Result**: Certificate subject will be `CN=<hostname>, O=My Development Team`.


### Example Configuration

**Development Server**:
```yaml
server:
  host: 0.0.0.0
  port: 60051
  http_port: 8080

  tls:
    enabled: true
    mode: self-signed
    self_signed:
      hostnames:
        - localhost
        - dev.example.com
      ip_addresses:
        - 127.0.0.1
        - 192.168.1.100
      validity_days: 365
      organization: Development Team
```

**Start Server**:
```bash
looms serve --config dev-config.yaml
```

**Expected Output**:
```
INFO  TLS enabled  mode=self-signed
INFO    Self-signed configuration  hostnames=[localhost dev.example.com]
INFO    IP addresses  ips=[127.0.0.1 192.168.1.100]
INFO    Generating self-signed certificate...
INFO    Certificate generated successfully
INFO    Certificate issuer  issuer=CN=Loom Self-Signed CA,O=Development Team
INFO    Certificate domains  domains=[localhost dev.example.com]
INFO    Certificate expiry  not_after=2026-12-11T10:00:00Z days_until_expiry=365
INFO  gRPC server TLS credentials applied
INFO  🚀 Loom server listening  grpc_addr=:60051 http_addr=:8080
```

**Client Connection**:
```bash
# Skip certificate verification (self-signed not trusted)
loom --tls --tls-insecure --server localhost:60051

# Or trust the generated CA certificate
loom --tls --tls-ca-file /var/loom/self-signed-ca.crt --server localhost:60051
```


### Generated Files

**Location**: `/var/loom/certs/` (or `$LOOM_DATA_DIR/certs/`)

**Files**:
- `self-signed-ca.crt` - CA certificate (trust this for clients)
- `self-signed-ca.key` - CA private key
- `<hostname>.crt` - Server certificate
- `<hostname>.key` - Server private key

**Trust CA Certificate** (for clients):
```bash
# macOS
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /var/loom/certs/self-signed-ca.crt

# Linux
sudo cp /var/loom/certs/self-signed-ca.crt /usr/local/share/ca-certificates/loom-self-signed.crt
sudo update-ca-certificates

# Windows
certutil -addstore -f "ROOT" C:\loom\certs\self-signed-ca.crt
```

**After trusting**: Clients can connect without `--tls-insecure`.


## Mutual TLS (mTLS)

### Overview

**Mutual TLS (mTLS)** provides two-way authentication:
- Server authenticates to client (standard TLS)
- Client authenticates to server with certificate (mTLS)

**Use Cases**:
- Zero-trust networks
- Service-to-service authentication
- Regulatory compliance (financial, healthcare)
- API gateway authentication


### Server Configuration

**Enable mTLS** (manual mode):
```yaml
server:
  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /etc/loom/certs/server.crt
      key_file: /etc/loom/certs/server.key
      ca_file: /etc/loom/certs/client-ca.crt  # CA for client certs
      require_client_cert: true               # Enforce mTLS
```

**Parameters**:
- `ca_file`: CA certificate that signed client certificates
- `require_client_cert`: `true` (enforce), `false` (optional)


### Client Certificate Setup

**1. Generate Client Certificate**:
```bash
# Generate client private key
openssl genrsa -out client.key 2048

# Create CSR
openssl req -new -key client.key -out client.csr \
    -subj "/CN=client-1/O=My Organization"

# Sign with CA (same CA as configured in server ca_file)
openssl x509 -req -in client.csr -CA client-ca.crt -CAkey client-ca.key \
    -CAcreateserial -out client.crt -days 365
```

**2. Configure Client**:

**TUI Client** (not directly supported, use gRPC client):
```go
import (
    "crypto/tls"
    "crypto/x509"
    "os"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
)

// Load client certificate
cert, err := tls.LoadX509KeyPair("client.crt", "client.key")
if err != nil {
    log.Fatalf("Failed to load client cert: %v", err)
}

// Load CA certificate
caCert, err := os.ReadFile("server-ca.crt")
if err != nil {
    log.Fatalf("Failed to read CA cert: %v", err)
}

certPool := x509.NewCertPool()
certPool.AppendCertsFromPEM(caCert)

// Create TLS config with client certificate
tlsConfig := &tls.Config{
    Certificates: []tls.Certificate{cert},
    RootCAs:      certPool,
    MinVersion:   tls.VersionTLS12,
}

// Create gRPC connection with mTLS
creds := credentials.NewTLS(tlsConfig)
conn, err := grpc.Dial("loom.example.com:60051", grpc.WithTransportCredentials(creds))
```


### Client Certificate Verification

**Server validates**:
1. Client certificate signed by trusted CA (`ca_file`)
2. Certificate not expired
3. Certificate chain valid

**On Failure**: Connection rejected with `tls: bad certificate` error.


### Optional mTLS

**Configuration** (`require_client_cert: false`):
```yaml
server:
  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /etc/loom/certs/server.crt
      key_file: /etc/loom/certs/server.key
      ca_file: /etc/loom/certs/client-ca.crt
      require_client_cert: false  # Optional mTLS
```

**Behavior**:
- Clients with valid certificates: Authenticated (mTLS)
- Clients without certificates: Allowed (standard TLS)

**Use Case**: Mixed environment (some clients support mTLS, others don't).


## Certificate Management

### Certificate Storage

**Location**: Depends on TLS mode

| Mode | Certificate Location | Private Key Location |
|------|---------------------|---------------------|
| manual | User-specified (`cert_file`) | User-specified (`key_file`) |
| letsencrypt | `<cache_dir>/<domain>.crt` | `<cache_dir>/<domain>.key` |
| self-signed | `/var/loom/certs/<hostname>.crt` | `/var/loom/certs/<hostname>.key` |

**Permissions** (critical):
```bash
# Certificate: Read by all (public)
chmod 644 server.crt

# Private key: Read by owner only (secret)
chmod 600 server.key
chown loom:loom server.key
```


### Certificate Validation

**Check Certificate**:
```bash
# View certificate details
openssl x509 -in server.crt -text -noout

# Check expiration
openssl x509 -in server.crt -noout -dates

# Verify certificate chain
openssl verify -CAfile ca.crt server.crt

# Check certificate matches key
openssl x509 -noout -modulus -in server.crt | openssl md5
openssl rsa -noout -modulus -in server.key | openssl md5
# Outputs should match
```


### Certificate Monitoring

**Monitor Expiration**:
```bash
#!/bin/bash
# check-cert-expiry.sh

CERT_FILE=/etc/loom/certs/server.crt
DAYS_WARN=30

# Get expiration date
EXPIRY=$(openssl x509 -in $CERT_FILE -noout -enddate | cut -d= -f2)
EXPIRY_EPOCH=$(date -d "$EXPIRY" +%s)
NOW_EPOCH=$(date +%s)
DAYS_UNTIL_EXPIRY=$(( ($EXPIRY_EPOCH - $NOW_EPOCH) / 86400 ))

if [ $DAYS_UNTIL_EXPIRY -lt $DAYS_WARN ]; then
    echo "WARNING: Certificate expires in $DAYS_UNTIL_EXPIRY days!"
    echo "Expiration: $EXPIRY"
fi
```

**Cron Job** (daily check):
```bash
0 8 * * * /usr/local/bin/check-cert-expiry.sh
```


### Certificate Backup

**Backup Certificates**:
```bash
# Backup directory
BACKUP_DIR=/backup/loom/certs/$(date +%Y%m%d)
mkdir -p $BACKUP_DIR

# Backup certificates and keys
cp /etc/loom/certs/*.crt $BACKUP_DIR/
cp /etc/loom/certs/*.key $BACKUP_DIR/

# Secure private keys
chmod 600 $BACKUP_DIR/*.key

# Optional: Encrypt backup
tar czf - $BACKUP_DIR | gpg --encrypt --recipient admin@example.com > loom-certs-backup.tar.gz.gpg
```


## TLS Manager

### Overview

The **TLS Manager** (`pkg/server/tls/manager.go`) handles:
- Certificate loading
- Certificate renewal (Let's Encrypt)
- Certificate status monitoring
- TLS configuration generation

**Lifecycle**:
1. **Start**: Load or generate certificates
2. **Run**: Monitor expiration, auto-renew (Let's Encrypt mode)
3. **Stop**: Cleanup


### TLS Manager Methods

```go
type Manager interface {
    // Start starts the TLS manager (blocking for initial cert acquisition)
    Start(ctx context.Context) error

    // Stop stops the TLS manager
    Stop(ctx context.Context) error

    // TLSConfig returns the current TLS configuration for gRPC server
    TLSConfig() *tls.Config

    // Status returns the current certificate status
    Status(ctx context.Context) (*loomv1.TLSStatus, error)

    // Renew manually triggers certificate renewal (Let's Encrypt mode only)
    Renew(ctx context.Context) error
}
```


### Certificate Status

**Query Status**:
```go
status, err := tlsManager.Status(ctx)
if err != nil {
    log.Fatalf("Failed to get TLS status: %v", err)
}

fmt.Printf("Mode: %s\n", status.Mode)
if status.Certificate != nil {
    fmt.Printf("Issuer: %s\n", status.Certificate.Issuer)
    fmt.Printf("Domains: %v\n", status.Certificate.Domains)
    fmt.Printf("Not Before: %s\n", time.Unix(status.Certificate.NotBefore, 0))
    fmt.Printf("Not After: %s\n", time.Unix(status.Certificate.NotAfter, 0))
    fmt.Printf("Days Until Expiry: %d\n", status.Certificate.DaysUntilExpiry)
}
```

**Status Fields**:
```protobuf
message TLSStatus {
  string mode = 1;  // manual | letsencrypt | self-signed
  CertificateInfo certificate = 2;
  bool auto_renew_enabled = 3;
  int32 renew_before_days = 4;
}

message CertificateInfo {
  string issuer = 1;
  repeated string domains = 2;
  int64 not_before = 3;
  int64 not_after = 4;
  int32 days_until_expiry = 5;
}
```


## Certificate Rotation

### Zero-Downtime Rotation

**Let's Encrypt Mode**: Automatic (no action required)

**Manual Mode**:
1. Obtain new certificate
2. Replace certificate files
3. Send `SIGHUP` to reload (future feature)
4. Current: Restart server

**Restart Server** (current method):
```bash
# Replace certificates
sudo cp new-server.crt /etc/loom/certs/server.crt
sudo cp new-server.key /etc/loom/certs/server.key

# Restart
sudo systemctl restart loom
```

**Future**: Hot reload with SIGHUP (no restart required).


### Renewal Testing

**Test Let's Encrypt Renewal**:
```bash
# Force renewal (development only)
# Requires direct access to TLS manager
```

**Staging Environment**:
```yaml
server:
  tls:
    letsencrypt:
      acme_directory_url: https://acme-staging-v02.api.letsencrypt.org/directory
      accept_tos: true
```

**Test Renewal**: Change `renew_before_days: 1000` to force renewal on next check.


## Testing TLS Connections

### Test gRPC TLS

```bash
# Test connection with grpcurl
grpcurl -v \
    -d '{"agent_id": "test"}' \
    loom.example.com:60051 \
    loom.v1.LoomService/GetHealth

# With custom CA
grpcurl -v \
    -cacert ca.crt \
    -d '{"agent_id": "test"}' \
    internal.example.com:60051 \
    loom.v1.LoomService/GetHealth

# Skip verification (testing only)
grpcurl -v -insecure \
    -d '{"agent_id": "test"}' \
    dev.example.com:60051 \
    loom.v1.LoomService/GetHealth
```


### Test HTTPS

```bash
# Test HTTP gateway
curl -v https://loom.example.com:8080/health

# With custom CA
curl -v --cacert ca.crt https://internal.example.com:8080/health

# Skip verification (testing only)
curl -v -k https://dev.example.com:8080/health
```


### Verify Certificate

```bash
# Check certificate presented by server
openssl s_client -connect loom.example.com:60051 -servername loom.example.com

# Check certificate chain
openssl s_client -connect loom.example.com:60051 -showcerts

# Check with custom CA
openssl s_client -connect internal.example.com:60051 -CAfile ca.crt
```


## Troubleshooting

### Issue: "certificate signed by unknown authority"

**Symptoms**:
- Client connection fails
- Error: `x509: certificate signed by unknown authority`

**Causes**:
1. Self-signed certificate (client doesn't trust CA)
2. Custom CA not in system trust store
3. Expired root CA certificate

**Resolution**:
```bash
# Option 1: Skip verification (dev only)
loom --tls --tls-insecure --server dev.example.com:60051

# Option 2: Trust custom CA
loom --tls --tls-ca-file ca.crt --server internal.example.com:60051

# Option 3: Add CA to system trust store
# macOS
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt

# Linux
sudo cp ca.crt /usr/local/share/ca-certificates/loom-ca.crt
sudo update-ca-certificates
```


### Issue: "certificate has expired or is not yet valid"

**Symptoms**:
- Connection fails
- Error: `x509: certificate has expired`

**Causes**:
1. Certificate expired
2. System clock incorrect

**Resolution**:
```bash
# 1. Check certificate expiration
openssl x509 -in cert.crt -noout -dates

# 2. Check system clock
date
# Should match current time

# 3. Renew certificate (if expired)
# Manual mode: Obtain new certificate
# Let's Encrypt: Wait for auto-renewal or force renewal
```


### Issue: "tls: bad certificate" (mTLS)

**Symptoms**:
- Client connection rejected
- Error: `tls: bad certificate`

**Causes**:
1. Client certificate not signed by trusted CA
2. Client certificate expired
3. Client certificate chain invalid

**Resolution**:
```bash
# 1. Verify client certificate
openssl x509 -in client.crt -text -noout

# 2. Check expiration
openssl x509 -in client.crt -noout -dates

# 3. Verify chain
openssl verify -CAfile ca.crt client.crt

# 4. Ensure CA matches server ca_file
diff ca.crt /path/to/server/ca_file.crt
```


### Issue: "server name does not match certificate"

**Symptoms**:
- Connection fails
- Error: `x509: certificate is valid for X, not Y`

**Causes**:
1. Connecting with IP address but certificate has only hostname
2. DNS name doesn't match certificate SAN

**Resolution**:
```bash
# Option 1: Connect with correct hostname
loom --tls --server loom.example.com:60051  # Not 192.168.1.100

# Option 2: Override server name verification (testing only)
loom --tls --tls-server-name loom.example.com --server 192.168.1.100:60051

# Option 3: Regenerate certificate with IP in SAN
# Add ip_addresses to self-signed config
```


### Issue: Let's Encrypt certificate request fails

**Symptoms**:
- Server fails to start
- Error: `Failed to obtain certificate from Let's Encrypt`

**Common Causes**:
1. Domain doesn't resolve to server IP
2. Port 80 not accessible
3. Firewall blocking HTTP

**Resolution**:
```bash
# 1. Check DNS
dig +short loom.example.com
# Should return server public IP

# 2. Check port 80 accessibility (from external network)
nc -zv loom.example.com 80

# 3. Check firewall
sudo ufw status | grep 80
sudo ufw allow 80/tcp

# 4. Test ACME challenge endpoint
curl http://loom.example.com/.well-known/acme-challenge/test
# Should connect (404 is OK)

# 5. Use staging environment for testing
# Change acme_directory_url to staging
```


## Best Practices

### 1. Use Let's Encrypt for Production

**Recommendation**: Let's Encrypt for all public production servers.

**Rationale**:
- Free, trusted certificates
- Automatic renewal (no expiration issues)
- Easy setup

**Configuration**:
```yaml
server:
  tls:
    enabled: true
    mode: letsencrypt
    letsencrypt:
      domains: [loom.example.com]
      email: admin@example.com
      accept_tos: true
```


### 2. Monitor Certificate Expiration

**Manual Mode**: Set up monitoring alert 30 days before expiration.

**Tools**:
- Cron job with email alert
- Monitoring system (Prometheus, Nagios)
- Cloud monitoring (AWS CloudWatch, GCP Monitoring)


### 3. Secure Private Keys

**File Permissions**:
```bash
chmod 600 server.key
chown loom:loom server.key
```

**Never**:
- ❌ Commit private keys to git
- ❌ Share private keys via email/chat
- ❌ Store in plaintext backups
- ❌ Use same key across environments


### 4. Use TLS 1.2+

**Minimum Version**: TLS 1.2 (enforced by Loom)

**Disable**: TLS 1.0, TLS 1.1 (deprecated, insecure)

**Verify**:
```bash
# Check TLS version
openssl s_client -connect loom.example.com:60051 -tls1_2
# Should succeed

openssl s_client -connect loom.example.com:60051 -tls1
# Should fail (TLS 1.0 disabled)
```


### 5. Use Strong Cipher Suites

**Loom Default**: Uses Go crypto/tls defaults (secure cipher suites only)

**Disabled by default**: RC4, 3DES, MD5, NULL ciphers

**Verify**:
```bash
# List supported ciphers
nmap --script ssl-enum-ciphers -p 60051 loom.example.com
```


### 6. Enable mTLS for Service-to-Service

**Recommendation**: Use mTLS for service-to-service communication in zero-trust networks.

**Configuration**:
```yaml
server:
  tls:
    enabled: true
    mode: manual
    manual:
      cert_file: /etc/loom/certs/server.crt
      key_file: /etc/loom/certs/server.key
      ca_file: /etc/loom/certs/service-ca.crt
      require_client_cert: true
```


### 7. Test with Staging (Let's Encrypt)

**Recommendation**: Test Let's Encrypt configuration with staging environment before production.

**Staging Config**:
```yaml
server:
  tls:
    letsencrypt:
      acme_directory_url: https://acme-staging-v02.api.letsencrypt.org/directory
      accept_tos: true
```

**After Testing**: Switch to production ACME URL.


### 8. Backup Certificates

**Recommendation**: Regular backups of certificates and private keys (encrypted).

**Script**:
```bash
# Encrypted backup
tar czf - /etc/loom/certs | gpg --encrypt --recipient admin@example.com > certs-backup.tar.gz.gpg
```


### 9. Rotate Certificates Regularly

**Recommendation**:
- Let's Encrypt: Automatic (90 days)
- Manual: Annually or semi-annually

**Process**: Test renewal process in staging before production.


### 10. Use Reverse Proxy for Advanced TLS

**Recommendation**: For advanced TLS features (OCSP stapling, TLS 1.3 0-RTT), use reverse proxy.

**Examples**:
- nginx
- Traefik
- Envoy

**Configuration** (nginx example):
```nginx
server {
    listen 443 ssl http2;
    server_name loom.example.com;

    ssl_certificate /etc/nginx/certs/loom.crt;
    ssl_certificate_key /etc/nginx/certs/loom.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # OCSP stapling
    ssl_stapling on;
    ssl_stapling_verify on;

    location / {
        grpc_pass grpc://localhost:60051;
    }
}
```

**Loom Configuration** (behind proxy):
```yaml
server:
  tls:
    enabled: false  # Proxy handles TLS
```


## Security Considerations

### Certificate Validation

**Always validate**:
- ✅ Certificate not expired
- ✅ Certificate chain valid
- ✅ Server name matches certificate CN/SAN
- ✅ Certificate signed by trusted CA

**Never**:
- ❌ Disable certificate verification in production (`--tls-insecure`)
- ❌ Accept self-signed certificates without explicit trust


### Private Key Security

**Protect private keys**:
- ✅ File permissions: `chmod 600`
- ✅ Owned by service account
- ✅ Never commit to version control
- ✅ Encrypt backups
- ✅ Rotate regularly

**If compromised**:
1. Revoke certificate immediately
2. Generate new key pair
3. Obtain new certificate
4. Update all servers


### TLS Termination

**Options**:
1. **Loom Server**: TLS handled by Loom (this guide)
2. **Reverse Proxy**: TLS handled by nginx/Traefik (Loom uses plain gRPC)
3. **Service Mesh**: TLS handled by Istio/Linkerd (transparent mTLS)

**Recommendation**: Reverse proxy for advanced features, Loom for simplicity.


### Perfect Forward Secrecy (PFS)

**Status**: Enabled by default (Go crypto/tls)

**Cipher Suites with PFS**: ECDHE-RSA-*, ECDHE-ECDSA-*

**Verify**:
```bash
openssl s_client -connect loom.example.com:60051 -cipher 'ECDHE'
# Should succeed

openssl s_client -connect loom.example.com:60051 -cipher 'RSA'
# Should fail or use PFS cipher
```


### HSTS (HTTP Strict Transport Security)

**Status**: Not implemented in Loom (HTTP gateway)

**Workaround**: Use reverse proxy (nginx) to add HSTS headers.

**nginx Example**:
```nginx
add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
```


## Error Codes

### ERR_TLS_CERT_NOT_FOUND

**Code**: `tls_cert_not_found`
**HTTP Status**: N/A (startup error)
**gRPC Code**: N/A

**Cause**: Certificate file not found (manual mode).

**Example**:
```
Error: failed to load certificate: open /etc/loom/certs/server.crt: no such file or directory
```

**Resolution**:
1. Verify file path in configuration
2. Check file exists: `ls -la /etc/loom/certs/server.crt`
3. Check permissions: `ls -ld /etc/loom/certs`

**Retry behavior**: Not retryable (fix configuration)


### ERR_TLS_KEY_NOT_FOUND

**Code**: `tls_key_not_found`
**HTTP Status**: N/A (startup error)
**gRPC Code**: N/A

**Cause**: Private key file not found (manual mode).

**Example**:
```
Error: failed to load private key: open /etc/loom/certs/server.key: no such file or directory
```

**Resolution**: Same as ERR_TLS_CERT_NOT_FOUND

**Retry behavior**: Not retryable (fix configuration)


### ERR_TLS_CERT_KEY_MISMATCH

**Code**: `tls_cert_key_mismatch`
**HTTP Status**: N/A (startup error)
**gRPC Code**: N/A

**Cause**: Certificate and private key don't match.

**Example**:
```
Error: tls: private key does not match public key
```

**Resolution**:
```bash
# Verify modulus matches
openssl x509 -noout -modulus -in server.crt | openssl md5
openssl rsa -noout -modulus -in server.key | openssl md5
# Should output same hash

# If mismatch, obtain correct key or certificate
```

**Retry behavior**: Not retryable (fix certificate/key)


### ERR_TLS_CERT_EXPIRED

**Code**: `tls_cert_expired`
**HTTP Status**: N/A (connection error)
**gRPC Code**: `UNAVAILABLE`

**Cause**: Certificate has expired.

**Example**:
```
Error: x509: certificate has expired or is not yet valid
```

**Resolution**:
```bash
# Check expiration
openssl x509 -in cert.crt -noout -dates

# Manual mode: Obtain new certificate
# Let's Encrypt: Check auto-renewal logs
```

**Retry behavior**: Not retryable until renewed


### ERR_TLS_UNKNOWN_AUTHORITY

**Code**: `tls_unknown_authority`
**HTTP Status**: N/A (connection error)
**gRPC Code**: `UNAVAILABLE`

**Cause**: Certificate signed by untrusted CA.

**Example**:
```
Error: x509: certificate signed by unknown authority
```

**Resolution**:
```bash
# Client: Trust CA certificate
loom --tls --tls-ca-file ca.crt --server internal.example.com:60051

# Or skip verification (dev only)
loom --tls --tls-insecure --server dev.example.com:60051
```

**Retry behavior**: Retryable after trusting CA


### ERR_TLS_HOSTNAME_MISMATCH

**Code**: `tls_hostname_mismatch`
**HTTP Status**: N/A (connection error)
**gRPC Code**: `UNAVAILABLE`

**Cause**: Server hostname doesn't match certificate CN/SAN.

**Example**:
```
Error: x509: certificate is valid for loom.example.com, not 192.168.1.100
```

**Resolution**:
```bash
# Option 1: Connect with correct hostname
loom --tls --server loom.example.com:60051

# Option 2: Override server name (testing only)
loom --tls --tls-server-name loom.example.com --server 192.168.1.100:60051

# Option 3: Regenerate certificate with IP in SAN
```

**Retry behavior**: Retryable after fixing hostname


### ERR_LETSENCRYPT_CHALLENGE_FAILED

**Code**: `letsencrypt_challenge_failed`
**HTTP Status**: N/A (startup error)
**gRPC Code**: N/A

**Cause**: Let's Encrypt HTTP-01 challenge failed.

**Example**:
```
Error: failed to complete HTTP-01 challenge: connection refused
```

**Resolution**:
1. Verify domain resolves to server: `dig +short loom.example.com`
2. Check port 80 accessible: `nc -zv loom.example.com 80`
3. Check firewall: `sudo ufw allow 80/tcp`
4. Test challenge endpoint: `curl http://loom.example.com/.well-known/acme-challenge/test`

**Retry behavior**: Retryable after fixing connectivity


### ERR_LETSENCRYPT_RATE_LIMIT

**Code**: `letsencrypt_rate_limit`
**HTTP Status**: N/A (startup error)
**gRPC Code**: N/A

**Cause**: Let's Encrypt rate limit exceeded.

**Example**:
```
Error: too many certificates already issued for: loom.example.com
```

**Resolution**:
1. Wait for rate limit reset (weekly)
2. Use staging environment for testing
3. Check rate limit status: https://crt.sh/?q=loom.example.com

**Retry behavior**: Retryable after rate limit resets


### ERR_MTLS_CLIENT_CERT_REQUIRED

**Code**: `mtls_client_cert_required`
**HTTP Status**: N/A (connection error)
**gRPC Code**: `UNAUTHENTICATED`

**Cause**: Server requires client certificate (mTLS) but client didn't provide one.

**Example**:
```
Error: tls: client didn't provide a certificate
```

**Resolution**: Provide client certificate (see mTLS section)

**Retry behavior**: Retryable with client certificate


## See Also

### Reference Documentation
- [TUI Reference](./tui.md) - Terminal UI with TLS support
- [CLI Reference](./cli.md) - `looms` server commands
- [gRPC API Reference](./grpc-api.md) - Protocol details

### Guides
- [Production Deployment Guide](../guides/production-deployment.md) - Production setup with TLS
- [Security Best Practices](../guides/security.md) - Comprehensive security guide

### Architecture Documentation
- [Communication Architecture](../architecture/communication-system.md) - gRPC/HTTP design

### External Resources
- [Let's Encrypt Documentation](https://letsencrypt.org/docs/) - ACME protocol, rate limits
- [Go crypto/tls Package](https://pkg.go.dev/crypto/tls) - TLS implementation
- [gRPC Authentication](https://grpc.io/docs/guides/auth/) - gRPC security guide
- [Mozilla SSL Configuration Generator](https://ssl-config.mozilla.org/) - TLS best practices
