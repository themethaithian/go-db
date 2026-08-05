# 0004-storage

## Status
Accepted

## Context
The application must store Profiles, passwords, and audit logs securely and durably.

Profiles contain host, port, username, and a reference to a password.
Passwords must never touch disk in plaintext—this is a hard constraint.
Audit logs must be appended reliably and never modified.

Different credential stores have different availability and security profiles.
Audit storage formats can evolve.

## Decision
**Profiles**: Stored as TOML in the user's config directory (OS-specific: `~/.config/go-db` on Linux, `~/Library/Application Support/go-db` on macOS, `%APPDATA%/go-db` on Windows).

**Passwords**: Retrieved from the OS keychain (a Keychain port abstracts the platform-specific implementations: macOS Keychain, Windows Credential Manager, Linux secret-service).
Passwords are never written to disk by go-db; they live only in the OS keychain.

**Audit log**: Append-only JSONL (one JSON object per line) written behind an AuditLog port.
This allows swapping the storage backend (e.g. SQLite in v2) without changing the audit contract.

## Alternatives rejected

**(a) Plaintext or encrypted password files**
  Never touches disk; OS keychain only. Reduces attack surface and leverages platform security.

**(b) SQLite audit store**
  JSONL suffices for v1. Database audit storage deferred to v2 when log volume justifies it.

## Consequences
- Profiles are human-readable and version-controllable (though no secrets touch them).
- Passwords are isolated from disk and managed by the OS, reducing our attack surface.
- Audit logs are human-readable, verifiable, and can be easily exported or analyzed.
- The Keychain and AuditLog ports enable testing with fakes and allow future swaps (e.g. encrypted keychain, database audit store).
