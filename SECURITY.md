# Security Policy

## Scope

impair is a test/benchmark tool: a deterministic network-impairment engine and
real-socket relay drivers. It is intended for use on trusted, local, or lab
networks. Security-relevant surface to be aware of:

- The `relay`/`ristrelay` drivers bind UDP sockets and forward traffic to a
  configured upstream address — treat the bind address as untrusted input and do
  not expose a relay to the public internet.
- The profile importers (`internal/profile`) and droplist replay parse external
  files; report any parser crash or unbounded allocation on malformed input.
- impair does not implement cryptography and is not a security control.

## Reporting a Vulnerability

**Please do NOT open public GitHub issues for security vulnerabilities.**

Instead, report vulnerabilities through [GitHub Security Advisories](https://github.com/zsiec/impair/security/advisories/new).

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

## Response

- **Acknowledgment**: Within 48 hours
- **Assessment**: Within 7 days
- **Fix target**: Within 30 days for confirmed vulnerabilities

## Supported Versions

Security fixes are applied to the latest release only.
