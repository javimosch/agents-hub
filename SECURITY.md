# Security

`ahub` is an early project and has not had an independent security audit.

## Reporting a vulnerability

Please report it privately through GitHub: **Security → Report a
vulnerability** on this repository. Do not open a public issue for it.

## Known gaps

These are known and not yet fixed. See [Limits](README.md#limits).

- The `cmd` and job tools are not a sandbox. Their arguments are limited to the
  operator's patterns, but a permitted command runs with the user's rights and
  inherits the environment, including any tokens in it.
- Self-updates are unsigned and are not checked for being newer.
