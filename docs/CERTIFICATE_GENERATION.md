# Certificate Generation in Mycorrizal

## Overview

Mycorrizal uses automatic certificate generation to secure QUIC-based cluster communication. Each node generates its own ephemeral TLS certificate at startup, signed by a shared Certificate Authority (CA). This provides strong mutual authentication between nodes without manual certificate distribution.

## Architecture

### Trust Model

```
┌─────────────────────────────────────┐
│     Certificate Authority (CA)      │
│   (Stored in 1Password)              │
│   - CA Certificate (public)          │
│   - CA Private Key (secret)          │
└──────────────┬──────────────────────┘
               │ Signs
               ├──────────────┬──────────────┬──────────────┐
               ▼              ▼              ▼              ▼
         ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐
         │ Node A  │    │ Node B  │    │ Node C  │    │ Node N  │
         │ Cert    │    │ Cert    │    │ Cert    │    │ Cert    │
         └─────────┘    └─────────┘    └─────────┘    └─────────┘
         Auto-gen       Auto-gen       Auto-gen       Auto-gen
         on startup     on startup     on startup     on startup
```

### Components

1. **Certificate Authority (CA)**
   - Single CA certificate and private key shared across all nodes
   - Stored securely in 1Password
   - Used to sign individual node certificates
   - Long-lived (1-5 years typical)

2. **Node Certificates**
   - Auto-generated per node on startup
   - Signed by the shared CA
   - Ephemeral (regenerated on each restart)
   - Valid for 1 year (though typically recreated much sooner)
   - Unique per node using nodeID as CommonName

3. **TLS Configuration**
   - Mutual TLS (mTLS): Both client and server authenticate
   - TLS 1.3 required (QUIC protocol requirement)
   - ALPN protocol: "mycorrizal"

## Certificate Structure

### CA Certificate

The CA certificate should have:
```
Subject:
  CommonName: Mycorrizal Cluster CA
  Organization: Your Organization

KeyUsage:
  - Certificate Signing
  - CRL Signing

BasicConstraints:
  CA: true
  PathLen: 0

Validity: 1-5 years
```

### Node Certificate

Auto-generated node certificates have:
```
Subject:
  CommonName: <nodeID>
  Organization: Mycorrizal Cluster

KeyUsage:
  - Digital Signature
  - Key Encipherment

ExtKeyUsage:
  - Server Authentication (for accepting connections)
  - Client Authentication (for initiating connections)

SubjectAlternativeNames:
  DNS: localhost, <nodeID>
  IP: 127.0.0.1

Validity: 1 year (from generation time)
```

## Implementation Flow

### 1. Startup Sequence

```go
// During node initialization (nodosum.New() or nodosum.Start())

1. Load CA from 1Password
   ├─ Fetch CA certificate (public)
   └─ Fetch CA private key (secret)

2. Generate node certificate
   ├─ Create RSA-2048 private key
   ├─ Build certificate template with nodeID
   ├─ Sign with CA private key
   └─ Create tls.Certificate

3. Configure TLS
   ├─ Set node certificate for outgoing connections
   ├─ Create CA cert pool for peer validation
   ├─ Enable mutual TLS (RequireAndVerifyClientCert)
   └─ Set TLS 1.3 and ALPN "mycorrizal"

4. Initialize QUIC transport with TLS config
```

### 2. Connection Handshake

```
Node A (Client)                          Node B (Server)
     │                                        │
     ├──── QUIC ClientHello ─────────────────>│
     │     (includes Node A certificate)      │
     │                                         │
     │<──── QUIC ServerHello ──────────────────┤
     │     (includes Node B certificate)       │
     │                                         │
     ├──── Verify Node B cert ────────┐       │
     │     (signed by CA?)             │       │
     │     (valid hostname/IP?)        │       │
     │     (not expired?)              │       │
     │<────────────────────────────────┘       │
     │                                         │
     │                         ┌──── Verify Node A cert
     │                         │     (signed by CA?)
     │                         │     (valid hostname/IP?)
     │                         │     (not expired?)
     │                         └────>│
     │                                         │
     │<══════ Encrypted QUIC Connection ══════>│
```

## Security Properties

### What This Provides

✅ **Mutual Authentication**
   - Both nodes verify each other's identity
   - Prevents unauthorized nodes from joining

✅ **Encryption**
   - All cluster traffic encrypted with TLS 1.3
   - Forward secrecy via ephemeral keys

✅ **Node Identity**
   - Each node has unique certificate with nodeID
   - Audit trail of which node made which connection

✅ **No Manual Certificate Management**
   - Certificates auto-generate on startup
   - No need to distribute individual certs to nodes

✅ **Short-Lived Credentials**
   - Certificates regenerated on restart
   - Limits exposure window if node compromised

### Attack Scenarios

❌ **Man-in-the-Middle (MITM)**
   - **Prevented**: Both nodes verify certificates signed by CA
   - Attacker cannot forge valid certificate without CA private key

❌ **Unauthorized Node Join**
   - **Prevented**: New node must have CA certificate + key to generate valid cert
   - Cannot connect without valid CA-signed certificate

⚠️ **CA Key Compromise**
   - **Risk**: If attacker obtains CA private key, they can generate valid certificates
   - **Mitigation**: Protect CA key in 1Password with strong access controls
   - **Recovery**: Rotate CA, redistribute to all nodes, restart cluster

⚠️ **Node Compromise**
   - **Risk**: Compromised node can use its certificate until it expires
   - **Mitigation**: Short-lived certificates (regenerate on restart)
   - **Recovery**: No built-in revocation; rotate CA if needed

## Key Management

### CA Key Lifecycle

**Creation** (One-time)
```bash
# Generate CA private key
openssl genrsa -out ca-key.pem 4096

# Generate CA certificate (5 year validity)
openssl req -new -x509 -days 1825 -key ca-key.pem -out ca-cert.pem \
  -subj "/CN=Mycorrizal Cluster CA/O=YourOrg"

# Store in 1Password (manual or via CLI)
op item create --category=SecureNote --title="mycorrizal-cluster-ca" \
  --vault="mycorrizal-infrastructure" \
  certificate[concealed]="$(cat ca-cert.pem)" \
  private-key[concealed]="$(cat ca-key.pem)"

# Securely delete local copies
shred -u ca-key.pem ca-cert.pem
```

**Rotation** (Every 1-5 years or on compromise)
```bash
# 1. Generate new CA
# 2. Update 1Password item
# 3. Rolling restart of all nodes (will pull new CA)
# 4. Old CA expires naturally
```

**Access Control**
- Restrict 1Password vault to node service accounts
- Use 1Password service accounts (not personal accounts)
- Enable audit logging to track CA access
- Principle of least privilege (read-only for nodes)

### Node Key Lifecycle

**Generation**: Automatic on startup
**Lifetime**: Until node restarts
**Rotation**: Restart the node
**Revocation**: Not supported (short-lived mitigates this)

## Configuration

### Required Config Fields

```go
type Config struct {
    // TLS/Security
    TlsEnabled       bool   // Enable TLS for QUIC
    OnePasswordVault string // 1Password vault name
    OnePasswordItem  string // 1Password item name

    // ... other fields
}
```

### Example Usage

```go
cfg := &Config{
    NodeId:           "node-abc123",
    TlsEnabled:       true,
    OnePasswordVault: "mycorrizal-infrastructure",
    OnePasswordItem:  "cluster-ca",
    QuicPort:         7947,
}

n, err := nodosum.New(cfg)
if err != nil {
    log.Fatal(err)
}

// TLS will be initialized automatically
err = n.Start()
```

## Troubleshooting

### Common Issues

**"failed to load CA from 1Password"**
- Check `OP_SERVICE_ACCOUNT_TOKEN` environment variable is set
- Verify vault and item names are correct
- Confirm service account has read access to the vault

**"failed to parse CA certificate"**
- Verify PEM format in 1Password (must include `-----BEGIN CERTIFICATE-----`)
- Check certificate is not corrupted

**"x509: certificate signed by unknown authority"**
- CA certificate not in peer's trust store
- Nodes using different CA certificates
- Check all nodes are pulling from same 1Password item

**"remote error: tls: bad certificate"**
- Node certificate expired (check system clock)
- Certificate not signed by expected CA
- Missing client/server auth in ExtKeyUsage

## Best Practices

### Development
- Use separate CA for dev/staging/prod environments
- Test certificate rotation regularly
- Monitor certificate expiration dates

### Production
- Rotate CA every 1-2 years
- Use 1Password service accounts (not personal)
- Enable audit logging for CA access
- Keep CA private key only in 1Password (never on disk)
- Use short certificate validity periods
- Implement monitoring for cert expiration

### Disaster Recovery
- Document CA rotation procedure
- Keep encrypted backup of CA in separate location
- Test CA rotation in staging environment first
- Have rollback plan for failed rotations

## Future Enhancements

**Potential Improvements**:
- Certificate Revocation List (CRL) support
- OCSP stapling for revocation checking
- Hardware Security Module (HSM) integration
- Automated CA rotation
- Per-node key pinning
- Certificate transparency logging

## References

- [RFC 5280 - X.509 Certificate Profile](https://tools.ietf.org/html/rfc5280)
- [RFC 9000 - QUIC Transport Protocol](https://tools.ietf.org/html/rfc9000)
- [RFC 8446 - TLS 1.3](https://tools.ietf.org/html/rfc8446)
- [QUIC-TLS Specification](https://tools.ietf.org/html/rfc9001)
