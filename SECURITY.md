# Security

`ahub` is an early project and has not had an independent security audit.

## Reporting a vulnerability

Please report it privately through GitHub: **Security → Report a
vulnerability** on this repository. Do not open a public issue for it.

## Known gaps

These are known and not yet fixed. See [Limits](README.md#limits).

- An allowed `cmd` or job prefix does not restrict the arguments that follow,
  and child processes inherit the environment.
- Self-updates are unsigned and are not checked for being newer.
