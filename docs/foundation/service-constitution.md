# Service Constitution

> The non-negotiable rules every OwnBase service must obey. These are the anti-lock-in guarantees, made concrete. A service that breaks any rule here does not ship, no matter how useful it is.

OwnBase provides services — auth, jobs, storage, a Git host — so a user (or their AI) doesn't have to reinvent them per service. That convenience is also the exact place lock-in usually creeps in. This document is the wall against that.

These rules apply to every OwnBase service and to any alternative a user swaps in for a capability (see Rule 3).

## The five rules

### Rule 1 — It can be removed

The user can remove any service. Removing a service must not break the Base or hold the rest of their system hostage. If other services depend on the capability it provided, removal must fail loudly and clearly — never silently corrupt — but the user always has the power to take it out.

### Rule 2 — It can be forked

Every service is delivered as code the user owns, not merely as an opaque image. They can read it, change it, and run their modified version. A service that cannot be forked because its real logic lives on a vendor's servers is forbidden.

### Rule 3 — It can be replaced

Services satisfy a **capability**, and capabilities have more than one possible provider. A user can swap the default implementation for a different one — including software OwnBase did not write — by changing the provider for that capability. Service boundaries are designed so this is realistic, not theoretical. See [architecture-principles.md](architecture-principles.md), principle 7.

### Rule 4 — Data stays accessible

The data a service holds belongs to the user and remains accessible in standard, documented, open formats (for example, a Postgres database they can connect to and dump). No proprietary, undocumented stores. Whatever happens to the service, the user can always get their data out, intact and usable.

### Rule 5 — It works standalone

Every service runs fully on the user's machine without phoning home. No service may have a runtime dependency on anything outside that machine being alive.

## The ownership boundary

**The user owns:** the server account, all service code, all data, all configuration, all secrets, all backups, all domains.

**OwnBase provides:** the daemon, service templates, and this documentation. That's it — there is no OwnBase-operated backend a Base depends on at runtime.

## Capabilities are declared in `ownbase.yaml`, not a separate manifest

There is no standalone manifest file a service must author. A service is declared exactly once, as an entry under `services:` in the Base's `ownbase.yaml` — and that entry's key **is** the capability name (see [integration-contract.md](../integration-contract.md) and [ownbase-yaml.md](../ownbase-yaml.md)):

```yaml
services:
  auth: # this key is the capability name — services that `requires: [auth]` depend on it
    source: services/auth
    ref: v1.0.0
    port: 8080
    requires:
      - postgres
```

The Dockerfile is the only build interface; `ownbase.yaml` is the only declaration surface. No separate manifest, registry metadata, or endpoint list is needed.

Because services depend on the capability name (`auth`) rather than the product that satisfies it (`ownbase-auth`), a user can swap what's declared under that key — for a different implementation, or their own fork — without rewriting anything that depends on it:

```yaml
services:
  auth:
    source: services/my-auth-fork # was services/auth
    ref: main
    port: 8080
```

That is what "replaceable" means in practice.

## Alternative providers

A service that is not part of OwnBase — forked, self-written, or taken from elsewhere — can stand in for any default service as long as it honestly satisfies all five rules above and can be declared under `ownbase.yaml` like any other service (a local bare repo with a Dockerfile). Using one never weakens these rules.

## The test

> If OwnBase vanished today, could the user still run, inspect, modify, replace, and extract every service on their Base?

If the answer for any service is no, that service violates this constitution and must be fixed or removed.
