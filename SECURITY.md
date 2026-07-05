# Security Policy

## Reporting Security Issues

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please use GitHub's private vulnerability reporting:

1. Go to the **Security** tab of this repository
2. Click **"Report a vulnerability"**
3. Fill out the form with details

I will respond within **48 hours** and work with you to understand and address the issue.

### What to Include

- Type of issue (e.g., container escape, credential leak, privilege escalation)
- Full paths of affected source files
- Step-by-step instructions to reproduce
- Proof-of-concept or exploit code (if possible)
- Impact assessment and potential attack scenarios

## Supported Versions

Only the latest version receives security updates. Please always use the most recent release.

## Security Best Practices for Contributors

1. **Never commit secrets** — use environment variables
2. **Validate all input** — especially from external sources
3. **Keep dependencies updated** — Dependabot is enabled on this repo
4. **Follow the principle of least privilege** in all code

## Credential Storage

Service credentials are encrypted at rest with AES-256-GCM. The master key is
stored in the OS keychain (macOS Keychain, Windows Credential Manager, Linux
Secret Service). If no OS keyring is available (e.g. a headless Linux host with
no Secret Service), the key falls back to a `0600` file (`.credential_key`) in
the app data directory, next to the encrypted database. On such hosts,
"encrypted at rest" only protects against another user on the machine, not
against anyone who can read your app-data directory — keep that directory (and
any backups of it) protected, and prefer a host with a working OS keyring.

The fleet worker API binds to `127.0.0.1` by default. Set the bind address to
`0.0.0.0` only if you intend to accept worker/mobile connections from your LAN;
that exposes a bearer-token-authenticated HTTP endpoint to your network.

## Contact

For security questions that aren't vulnerabilities, open a regular issue.
