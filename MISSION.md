# Mission

> AI makes it easy to build software. OwnBase makes it easy to own it.

When any other document in this repo conflicts with this one, this one wins.

## The gap we exist to close

For the entire history of software, creating it was the hard part. You needed years of training, a team, and a budget — so most people rented software instead of owning it. That is what SaaS is: renting capability by the seat, by the month, forever.

AI broke the first half of that. A person who can describe what they want can now build it. The cost of *creating* software is collapsing toward zero.

The second half did not move. Once software exists, someone has to keep it alive: secure it, patch it, back it up, monitor it, renew its certificates, recover it when it breaks. That work did not get cheaper — and for most of the new builders, it was never even possible. They can create software they cannot responsibly own.

> The cost of creating software is collapsing.
> The cost of owning software is not.
> OwnBase exists to close that gap.

## What OwnBase is

OwnBase turns a machine the user controls into a secure, self-maintaining home for everything they build — their **Base**. They own the server, the code, the data, the config, the secrets, and the backups. We provide the daemon that keeps it healthy.

We are not a host. We are not a platform you build inside of and cannot leave. We are the operations engineer that sits between AI-built software and a production machine — except this engineer never sleeps, never quits, and hands you the keys to everything.

## Who it is for

Someone who can build software but does not want to become an infrastructure engineer. AI gave them the ability to create; what they lack — and do not want to acquire — is the patience for firewalls, package upgrades, certificate renewals, and 2 a.m. recovery. What they want is not "deploy my software." It is **"give my software a safe home and keep it alive so I can keep building."**

We do not optimize for platform engineers who want knobs, for Kubernetes shops, or for anyone who wants a black box they can never look inside. Every knob added for an expert is a way for the real user to cut themselves.

## The promise, in three lines

> Everything is yours.
> Nothing is mysterious.
> OwnBase just keeps it working.

## The hard constraints

These are the promise made operational. They do not change without a deliberate, explicit decision.

| Constraint | Detail |
|---|---|
| User owns everything | Code, server, data, config, secrets, backups, domains. Never trap a user. |
| Nothing is mysterious | Plain files, Git as source of truth, human- and AI-readable layouts. No black boxes. |
| Operations disappear | If a user must learn Linux, Docker, nginx, or certs because of this system, it failed. |
| Every service is ownable | Removable, forkable, replaceable, data accessible, runs without any OwnBase-operated cloud. |
| Boring technology wins | Ubuntu, Podman, Postgres, Git, Caddy. Never Kubernetes. |
| No pre-built application images | Every service is built locally from source at a pinned `ref:`. |

The reasoning behind each lives in [docs/foundation/](docs/foundation/).

## What success looks like

A user owns dozens of applications, agents, automations, and internal tools, most of them built by describing what they wanted to an AI. Everything runs on a Base they own. They never think about firewalls, patches, backups, certificates, containers, or Linux. When they want something new, they say "build this and put it on my Base," and it appears. When they want to leave, they can — it is just Ubuntu, and they have always had the keys.

They do not describe OwnBase as software. They describe it as *"the thing that keeps my software healthy."*

## What failure looks like

Failure is becoming the thing we exist to replace. We have failed if:

- Users learn Linux because of us.
- Users become DevOps engineers because of us.
- Users feel trapped and cannot leave with everything intact.
- We become another dashboard, another PaaS, another hosting bill.

The deepest failure mode is subtle: shipping convenience that quietly takes ownership away. There are two kinds of convenience. The kind we refuse is convenience *bought with ownership* — the smoother demo that traps you. The kind we build is convenience that *comes from* ownership — an AI starting from a stack the user owns, so they ship faster. We never trade the second for the first.

## The test we apply to everything

> Does this make the user **more** of an owner and **less** of a sysadmin?

If yes, it is probably right. If it makes them more of an owner but more of a sysadmin, redesign it until the sysadmin part disappears. If it makes them less of a sysadmin by making them less of an owner, reject it — that is SaaS, and the world has enough of it.

## The ambition

The first wave of SaaS let people *buy* software they could not build. The AI wave lets people *build* software they could not buy. The missing layer is where all that software lives and who keeps it alive.

> AI made software creation universal. OwnBase makes software ownership universal.
