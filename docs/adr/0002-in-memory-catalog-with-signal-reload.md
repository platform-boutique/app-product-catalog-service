# 2. The catalog is read once into memory and reloaded by signal

Status: accepted (reconstructed from the code and the earlier README).

## Context

The catalog is small (117 products) and changes only when the seed changes. Every page of the shop reads it.

## Decision

Load all products and departments into memory at start-up and answer every read from that copy. A reload is armed by `SIGUSR1` (the next request re-reads Postgres, then the flag clears) and disarmed by `SIGUSR2`. If the cache is ever empty, a request re-queries Postgres.

## Consequences

- Reads cost no SQL, and filter, sort and paging are computed in memory.
- The cache is per replica and changes are not visible until a reload or a restart.
- A reload does not run migrations or the seed.
