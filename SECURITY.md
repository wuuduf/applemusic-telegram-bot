# Security Policy

## Supported Versions

Security fixes are provided for:
- `main` branch (latest code)
- the latest release tag

Older versions may not receive patches.

## Reporting a Vulnerability

Please report security issues privately before opening a public issue:
- Preferred: GitHub Security Advisory (private report).
- Fallback: open an issue with minimal details and no exploit data, then coordinate disclosure.

When reporting, include:
- affected version/commit
- reproduction steps
- impact and expected risk
- logs with secrets removed

## Secret Handling

- Never publish `telegram-bot-token`, `media-user-token`, cookies, or private endpoints.
- If a secret is leaked, rotate it immediately and purge/rewrite history when needed.

