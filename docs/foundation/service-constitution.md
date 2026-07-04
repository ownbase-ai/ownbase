# Service Constitution

> The non-negotiable rules every OwnBase service must obey. These are the anti-lock-in guarantees, made concrete. A service that breaks any rule here does not ship, no matter how useful it is.

OwnBase provides services — auth, jobs, storage, a Git host — so a customer (or their AI) doesn't have to reinvent them per app. That convenience is also the exact place lock-in usually creeps in. This document is the wall against that.

These rules apply to every OwnBase service and to any alternative a customer swaps in for a capability (see Rule 3).

## The five rules

### Rule 1 — It can be removed

The customer can remove any service. Removing a service must not break the Base or hold the rest of their system hostage. If apps depend on the capability it provided, removal must fail loudly and clearly — never silently corrupt — but the customer always has the power to take it out.

### Rule 2 — It can be forked

Every service is delivered as code the customer owns, not merely as an opaque image. They can read it, change it, and run their modified version. A service that cannot be forked because its real logic lives on a vendor's servers is forbidden.

### Rule 3 — It can be replaced

Services satisfy a **capability**, and capabilities have more than one possible provider. A customer can swap the default implementation for a different one — including software OwnBase did not write — by changing the provider for that capability. Service boundaries are designed so this is realistic, not theoretical. See [architecture-principles.md](architecture-principles.md), principle 7.

### Rule 4 — Data stays accessible

The data a service holds belongs to the customer and remains accessible in standard, documented, open formats (for example, a Postgres database they can connect to and dump). No proprietary, undocumented stores. Whatever happens to the service, the customer can always get their data out, intact and usable.

### Rule 5 — It works standalone

Every service runs fully on the customer's machine without phoning home. No service may have a runtime dependency on anything outside that machine being alive.

## The ownership boundary

**The customer owns:** the server account, all app code, all service code, all data, all configuration, all secrets, all backups, all domains.

**OwnBase provides:** the daemon, service templates, and this documentation. That's it — there is no OwnBase-operated backend a Base depends on at runtime.

## The service manifest contract

Every OwnBase service declares itself in a standard manifest so apps depend on capabilities, the daemon can reason about it, and any AI can use it without guessing. Illustrative shape:

```yaml
name: ownbase-auth
type: service

provides:
  - auth
  - users
  - sessions
  - api_keys

requires:
  - postgres

endpoints:
  public:
    - /login
    - /logout
  internal:
    - /verify
    - /users
```

Because apps reference the capability (`auth`) rather than the product (`ownbase-auth`), a customer can move from:

```yaml
auth:
  provider: ownbase-auth
```

to a different provider:

```yaml
auth:
  provider: authentik
```

or to their own fork:

```yaml
auth:
  provider: custom
  source: ./services/my-auth
```

without rewriting their apps. That is what "replaceable" means in practice.

## Alternative providers

A service that is not part of OwnBase — forked, self-written, or taken from elsewhere — can stand in for any default service as long as it honestly satisfies all five rules above and publishes a compatible manifest. Using one never weakens these rules.

## The test

> If OwnBase vanished today, could the customer still run, inspect, modify, replace, and extract every service on their Base?

If the answer for any service is no, that service violates this constitution and must be fixed or removed.
