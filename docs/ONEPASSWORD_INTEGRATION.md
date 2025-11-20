# 1Password Integration in Mycorrizal

## Overview

Mycorrizal uses 1Password as a secure secret management solution for cluster credentials. This includes the CA certificate, CA private key, shared secrets for memberlist, and other sensitive configuration data.

## Why 1Password?

### Benefits

✅ **Centralized Secret Management**
   - Single source of truth for all cluster secrets
   - No secrets committed to git repositories
   - Easy updates propagate to all nodes

✅ **Security**
   - Encryption at rest and in transit
   - Fine-grained access control
   - Audit logging of all access
   - MFA support for human access

✅ **Developer Experience**
   - CLI integration for automation
   - Connect API for production systems
   - Cross-platform support
   - Team collaboration features

✅ **Compliance**
   - SOC 2 Type II certified
   - Audit trails for compliance requirements
   - Secret rotation tracking

## Integration Methods

Mycorrizal supports two methods of accessing 1Password:

### 1. CLI Method (Development & CI/CD)

Uses the `op` command-line tool.

**Pros**:
- Simple setup
- Works in CI/CD pipelines
- No additional infrastructure

**Cons**:
- Requires `op` CLI installed
- Slightly slower (process execution overhead)

**Use Cases**:
- Local development
- CI/CD pipelines
- Container initialization scripts

### 2. Connect API Method (Production)

Uses 1Password Connect Server for API-based access.

**Pros**:
- High performance (direct HTTP API)
- No CLI dependency
- Built for production use
- Better for Kubernetes/containerized environments

**Cons**:
- Requires Connect Server deployment
- Additional infrastructure

**Use Cases**:
- Production clusters
- Kubernetes deployments
- High-throughput scenarios

## Setup

### Prerequisites

1. **1Password Account**
   - Business or Team account
   - Create a vault for Mycorrizal secrets

2. **Service Account** (Recommended)
   - Create dedicated service account for nodes
   - Grants read-only access to specific vaults
   - Not tied to individual user accounts

### CLI Setup

#### 1. Install 1Password CLI

**macOS**:
```bash
brew install --cask 1password-cli
```

**Linux**:
```bash
# Download from https://developer.1password.com/docs/cli/get-started/
curl -sSfO https://cache.agilebits.com/dist/1P/op2/pkg/v2.XX.X/op_linux_amd64_v2.XX.X.zip
unzip op_linux_amd64_v2.XX.X.zip
sudo mv op /usr/local/bin/
```

**Verify**:
```bash
op --version
```

#### 2. Create Service Account

1. Log in to 1Password web interface
2. Navigate to Settings → Service Accounts
3. Create new service account: `mycorrizal-nodes`
4. Grant read access to vault: `mycorrizal-infrastructure`
5. Save the service account token (starts with `ops_`)

#### 3. Configure Environment

```bash
# Set service account token
export OP_SERVICE_ACCOUNT_TOKEN="ops_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

# Test access
op vault list
op item get mycorrizal-cluster-ca --vault mycorrizal-infrastructure
```

#### 4. Create Secret Items

**CA Certificate Item**:
```bash
op item create \
  --category=SecureNote \
  --title="mycorrizal-cluster-ca" \
  --vault="mycorrizal-infrastructure" \
  certificate[concealed]="$(cat ca-cert.pem)" \
  private-key[concealed]="$(cat ca-key.pem)"
```

**Verify**:
```bash
op read "op://mycorrizal-infrastructure/mycorrizal-cluster-ca/certificate"
```

### Connect API Setup

#### 1. Deploy 1Password Connect

**Docker**:
```bash
docker run -d \
  --name onepassword-connect \
  -p 8080:8080 \
  -v /path/to/credentials.json:/home/opuser/.op/1password-credentials.json \
  -v /path/to/data:/home/opuser/.op/data \
  1password/connect-api:latest
```

**Kubernetes**:
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: op-credentials
type: Opaque
data:
  1password-credentials.json: <base64-encoded-credentials>
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: onepassword-connect
spec:
  replicas: 2
  selector:
    matchLabels:
      app: onepassword-connect
  template:
    metadata:
      labels:
        app: onepassword-connect
    spec:
      containers:
      - name: connect-api
        image: 1password/connect-api:1.7
        ports:
        - containerPort: 8080
        volumeMounts:
        - name: credentials
          mountPath: /home/opuser/.op
          readOnly: true
      volumes:
      - name: credentials
        secret:
          secretName: op-credentials
```

#### 2. Configure Access Token

1. Generate Connect token via 1Password web interface
2. Set environment variable:

```bash
export OP_CONNECT_HOST="http://localhost:8080"
export OP_CONNECT_TOKEN="your-connect-token"
```

#### 3. Test Connection

```bash
curl -H "Authorization: Bearer $OP_CONNECT_TOKEN" \
  $OP_CONNECT_HOST/v1/vaults
```

## Usage in Mycorrizal

### Configuration

```go
type Config struct {
    // 1Password configuration
    OnePasswordVault     string // Vault name (e.g., "mycorrizal-infrastructure")
    OnePasswordItem      string // Item name (e.g., "mycorrizal-cluster-ca")
    OnePasswordMethod    string // "cli" or "connect"
    OnePasswordConnectURL string // Connect API URL (if using Connect)
}
```

### Environment Variables

**For CLI Method**:
```bash
export OP_SERVICE_ACCOUNT_TOKEN="ops_xxxxxxxx..."
```

**For Connect Method**:
```bash
export OP_CONNECT_HOST="http://onepassword-connect:8080"
export OP_CONNECT_TOKEN="xxxxxxxx..."
```

### Code Integration

The `cert.go` file provides the integration:

```go
// Load CA from 1Password
caCert, caKey, err := loadCAFromOnePassword(
    cfg.OnePasswordVault,  // "mycorrizal-infrastructure"
    cfg.OnePasswordItem,   // "mycorrizal-cluster-ca"
)
```

## Secret Structure

### Recommended Vault Organization

```
Vault: mycorrizal-infrastructure
│
├─ Item: mycorrizal-cluster-ca
│  ├─ Field: certificate (concealed)
│  │         -----BEGIN CERTIFICATE-----
│  │         ... CA certificate PEM ...
│  │         -----END CERTIFICATE-----
│  │
│  └─ Field: private-key (concealed)
│            -----BEGIN RSA PRIVATE KEY-----
│            ... CA private key PEM ...
│            -----END RSA PRIVATE KEY-----
│
├─ Item: mycorrizal-cluster-config
│  ├─ Field: shared-secret (concealed)
│  │         "your-shared-secret-for-memberlist"
│  │
│  ├─ Field: quic-port (text)
│  │         "7947"
│  │
│  └─ Field: discovery-host (text)
│            "consul.example.com:8500"
│
└─ Item: mycorrizal-cluster-ca-prod (separate CA for production)
   ├─ Field: certificate (concealed)
   └─ Field: private-key (concealed)
```

### Field Naming Conventions

- Use lowercase with hyphens: `shared-secret`, `private-key`
- Mark sensitive fields as `concealed`
- Use descriptive names, not abbreviations
- Include format hints in notes (e.g., "PEM format")

## Access Control

### Service Account Permissions

**Development Service Account**: `mycorrizal-dev`
- Read access to `mycorrizal-dev` vault
- Used by developers and CI/CD

**Staging Service Account**: `mycorrizal-staging`
- Read access to `mycorrizal-staging` vault
- Used by staging environment nodes

**Production Service Account**: `mycorrizal-prod`
- Read access to `mycorrizal-prod` vault only
- Used by production nodes
- Regularly rotated tokens
- Audit logging enabled

### Principle of Least Privilege

```
Service Account Permissions:
├─ mycorrizal-nodes-dev
│  └─ Read: mycorrizal-dev vault
│
├─ mycorrizal-nodes-staging
│  └─ Read: mycorrizal-staging vault
│
└─ mycorrizal-nodes-prod
   └─ Read: mycorrizal-prod vault

Human Users:
├─ Developers
│  ├─ Read/Write: mycorrizal-dev vault
│  └─ Read: mycorrizal-staging vault
│
└─ Operators
   ├─ Read/Write: mycorrizal-staging vault
   └─ Read/Write: mycorrizal-prod vault (with MFA)
```

## Operational Procedures

### Secret Rotation

**CA Certificate Rotation** (Annual or on compromise):
```bash
# 1. Generate new CA
openssl genrsa -out ca-key-new.pem 4096
openssl req -new -x509 -days 1825 -key ca-key-new.pem -out ca-cert-new.pem

# 2. Update 1Password item
op item edit mycorrizal-cluster-ca \
  certificate[concealed]="$(cat ca-cert-new.pem)" \
  private-key[concealed]="$(cat ca-key-new.pem)"

# 3. Rolling restart of all nodes
kubectl rollout restart deployment mycorrizal-nodes

# 4. Securely delete local files
shred -u ca-key-new.pem ca-cert-new.pem
```

**Service Account Token Rotation** (Quarterly):
```bash
# 1. Create new service account in 1Password web UI
# 2. Update environment variables/secrets in deployment
# 3. Verify new token works
# 4. Delete old service account
```

### Monitoring

**Audit Logging**:
```bash
# View access logs in 1Password web interface
# Settings → Activity Log → Filter by service account
```

**Alerts**:
- Set up alerts for:
  - Failed authentication attempts
  - Unusual access patterns
  - Secret modifications
  - Service account creation/deletion

### Disaster Recovery

**Backup Procedures**:
1. Export vault to encrypted file (quarterly)
2. Store encrypted backup in separate location
3. Test restore procedure annually

**Recovery Steps**:
```bash
# If 1Password unavailable:
1. Restore from encrypted backup
2. Import secrets into new 1Password vault
3. Create new service accounts
4. Update node environment variables
5. Restart cluster
```

## Security Best Practices

### Do's ✅

- **Use service accounts** for automated access
- **Rotate tokens regularly** (quarterly minimum)
- **Use separate vaults** for dev/staging/prod
- **Enable audit logging** and review regularly
- **Use MFA** for human administrator access
- **Mark sensitive fields** as concealed
- **Test disaster recovery** procedures
- **Document access procedures**

### Don'ts ❌

- **Don't commit tokens** to git repositories
- **Don't share service accounts** across environments
- **Don't use personal accounts** for automation
- **Don't store secrets** in environment variables in code
- **Don't log secret values** (even in debug mode)
- **Don't grant write access** to service accounts unnecessarily
- **Don't skip rotation** schedules

## Troubleshooting

### Common Issues

**"401 Unauthorized" when using CLI**
```bash
# Check token is set
echo $OP_SERVICE_ACCOUNT_TOKEN

# Verify token is valid
op vault list

# Check token has access to vault
op vault get mycorrizal-infrastructure
```

**"Item not found"**
```bash
# List items in vault
op item list --vault mycorrizal-infrastructure

# Check exact item name (case-sensitive)
op item get "mycorrizal-cluster-ca" --vault mycorrizal-infrastructure
```

**"Network error" with Connect API**
```bash
# Test connectivity
curl $OP_CONNECT_HOST/health

# Check token
curl -H "Authorization: Bearer $OP_CONNECT_TOKEN" \
  $OP_CONNECT_HOST/v1/vaults

# Verify Connect server logs
docker logs onepassword-connect
```

**Certificate parse errors**
```bash
# Verify PEM format
op read "op://mycorrizal-infrastructure/mycorrizal-cluster-ca/certificate" | head -n 1
# Should output: -----BEGIN CERTIFICATE-----

# Check for extra whitespace/newlines
op read "op://mycorrizal-infrastructure/mycorrizal-cluster-ca/certificate" | wc -l
```

## Performance Considerations

### CLI Method
- **Latency**: ~500ms per secret fetch (process overhead)
- **Caching**: Consider caching secrets in memory (never disk)
- **Rate Limits**: 1Password has rate limits; batch operations when possible

### Connect API Method
- **Latency**: ~50ms per secret fetch (HTTP request)
- **Throughput**: Higher concurrent request limit
- **Caching**: Connect server caches locally
- **Recommended**: For production with many nodes

### Optimization Tips

```go
// Cache secrets after first load (in-memory only)
type SecretCache struct {
    caCert     *x509.Certificate
    caKey      *rsa.PrivateKey
    loadedAt   time.Time
    mu         sync.RWMutex
}

// Refresh cache every 24 hours
func (c *SecretCache) GetCA() (*x509.Certificate, *rsa.PrivateKey, error) {
    c.mu.RLock()
    if time.Since(c.loadedAt) < 24*time.Hour && c.caCert != nil {
        defer c.mu.RUnlock()
        return c.caCert, c.caKey, nil
    }
    c.mu.RUnlock()

    // Reload from 1Password
    // ...
}
```

## References

- [1Password CLI Documentation](https://developer.1password.com/docs/cli/)
- [1Password Connect Documentation](https://developer.1password.com/docs/connect/)
- [1Password Service Accounts](https://developer.1password.com/docs/service-accounts/)
- [1Password Security Model](https://1password.com/security/)

## Support

For 1Password-specific issues:
- Documentation: https://developer.1password.com/
- Support: https://support.1password.com/
- Community: https://1password.community/
