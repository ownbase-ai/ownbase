# Mission

> Build faster with AI. Own everything.

When any other document in this repo conflicts with this one, this one wins.

## The gap this closes

AI made building software fast. Running it did not get fast.

A coding agent can produce a working application in an afternoon, then hit the part that did not change: a provider console, a DNS panel, a managed database with its own dashboard, a deploy service with its own API, a secrets store, a billing account. Every one of those is a boundary the agent cannot see through. It can call the endpoints a vendor chose to expose and nothing else — it cannot read the log, inspect the process, or check whether what it just did actually worked. Most of what makes agents unreliable in deployment is missing feedback and missing permission, and no amount of model quality fixes either.

So the bottleneck moved. It used to be writing the code. Now it is everything after.

Underneath that sits an older problem. For the entire history of software, creating it was the hard part — you needed years of training, a team, and a budget, so most people rented software instead of owning it. That is what SaaS is: renting capability by the seat, by the month, forever. AI broke that half. The other half did not move. Once software exists, someone still has to secure it, patch it, back it up, renew its certificates, and recover it at 2 a.m. — and for most of the new builders that work was never possible in the first place. They can create software they cannot responsibly own.

> The cost of creating software collapsed.
> The cost of owning it did not.
> That gap is where AI-built software now stalls.

## What OwnBase is

OwnBase is open-source software (MIT) that turns a machine you control into a secure, self-maintaining home for everything you build — your **Base**. A daemon runs on that machine and does the operations work: the firewall, the patches, the certificates, the backups, the recovery.

It is not a host, not a platform you build inside and cannot leave, and not a service anything on a Base depends on at runtime. There is no account, no control plane, no vendor backend.

You own the server, the code, the data, the config, the secrets, the backups, the domains — and OwnBase itself. Fork it, change it, run your own build. Nothing here can be withdrawn, because there is nobody in a position to withdraw it.

## Why ownership is the mechanism, not a second feature

Speed and ownership look like two separate benefits. They are one.

An agent is fast on a machine it owns for the same reason it is slow behind a vendor API: it can see all of it. Root on a real computer means reading the actual log, restarting the actual process, standing up a database, and verifying its own work instead of reporting success and hoping. Every layer taken away from the owner is also a layer taken away from their agent.

This is why the constraints below are not a tax on velocity. They are what produces it. Any change that quietly reduces ownership is also a change that makes the machine harder for an agent to operate, and it should be rejected on both counts.

## Who it is for

Someone who can build software but does not want to become an infrastructure engineer. AI gave them the ability to create; what they lack — and do not want to acquire — is the patience for firewalls, package upgrades, certificate renewals, and 2 a.m. recovery. What they want is not "deploy my software." It is **"give my software a safe home and keep it alive so I can keep building."**

OwnBase is not optimized for platform engineers who want knobs, for Kubernetes shops, or for anyone who wants a black box they can never look inside. Every knob added for an expert is a way for the real user to cut themselves.

## The promise, in three lines

> Everything is yours.
> Nothing is mysterious.
> It just keeps working.

## The hard constraints

These are the promise made operational. They do not change without a deliberate, explicit decision.

| Constraint | Detail |
|---|---|
| User owns everything | Code, server, data, config, secrets, backups, domains — and OwnBase itself, MIT-licensed and forkable. Never trap a user. |
| Nothing is mysterious | Plain files, Git as source of truth, human- and AI-readable layouts. No black boxes. |
| Operations disappear | If a user must learn Linux, Docker, nginx, or certs because of this system, it failed. |
| Every service is ownable | Removable, forkable, replaceable, data accessible, runs without any OwnBase-operated cloud. |
| Boring technology wins | Ubuntu, Podman, Postgres, Git, Caddy. Never Kubernetes. |
| No pre-built application images | Every service is built locally from source at a pinned `ref:`. |

The reasoning behind each lives in [docs/foundation/](docs/foundation/).

## What success looks like

A user owns dozens of applications, agents, automations, and internal tools, most of them built by describing what they wanted to an AI. Everything runs on a Base they own. They never think about firewalls, patches, backups, certificates, containers, or Linux. When they want something new, they say "build this and put it on my Base," and it appears — not eventually, but in the same session, because the agent doing it has the whole machine and needs nobody's permission. When they want to leave, they can: it is just Ubuntu, and they have always had the keys.

They do not describe OwnBase as software. They describe it as *"the thing that keeps my software healthy."*

## What failure looks like

Failure is becoming the thing OwnBase exists to replace. It has failed if:

- Users learn Linux because of it.
- Users become DevOps engineers because of it.
- Users feel trapped and cannot leave with everything intact.
- It becomes another dashboard, another PaaS, another hosting bill.

The deepest failure mode is subtle: shipping convenience that quietly takes ownership away. There are two kinds of convenience. The kind bought *with* ownership is the smoother demo that traps you, and it is always refused. The kind that comes *from* ownership — an agent working on a machine it can see all of, so the work lands faster — is the entire point. The second is never traded for the first.

## The test applied to everything

> Does this make the user **more** of an owner and **less** of a sysadmin?

If yes, it is probably right. If it makes them more of an owner but more of a sysadmin, redesign it until the sysadmin part disappears. If it makes them less of a sysadmin by making them less of an owner, reject it — that is SaaS, and the world has enough of it.

There is no third axis for speed. Ownership is how the speed is obtained, so a change that fails this test is already a change that slows the machine down.

## The ambition

The first wave of SaaS let people *buy* software they could not build. The AI wave lets people *build* software they could not buy. The missing layer is where all that software lives, who keeps it alive, and whether the agent that built it is allowed to operate it.

> AI made software creation universal. OwnBase makes software ownership universal.
