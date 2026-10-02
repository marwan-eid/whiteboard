# ADR-0006: Hosting on a $0 budget

- **Status:** Accepted (revised 2026-09-27 with MilkRun's measured usage)
- **Date:** 2026-09-27

## Context
The public demo needs to:
- load instantly, with no cold starts,
- keep persistent WebSockets open,
- run several nodes plus Postgres and monitoring.

**Budget: strictly $0.** No paid fallback. When free resources run short, we cut features or numbers instead of paying. Adding a card for identity verification is fine.

## Options
| Option | Pros | Cons |
|---|---|---|
| **Oracle Cloud Always Free (Ampere A1)** | 4 OCPU, 24 GB RAM, 200 GB block storage, 10 TB/month egress, and 20 GB object storage, all free. | A1 capacity is sometimes unavailable in a region. Oracle reclaims idle Always Free instances. |
| **Render / Railway / Fly.io free tiers** | Easy deploys. | Free services sleep, or the free tier is gone. Sleeping breaks "loads instantly", and memory is too small for 1,000 sockets plus Postgres. |
| **Home server + Cloudflare Tunnel** | Free, and the hardware is under our control. | Uptime depends on home power and internet. Not credible for a public demo. |

## Decision
**Oracle Cloud Always Free, Ampere A1.**

**Infrastructure:**
- **Provisioning:** Terraform (OCI provider) plus cloud-init installs Docker.
- **Services:** Docker Compose runs Caddy, 2–3 app nodes, Postgres 17, Prometheus, and (if memory allows) Grafana.
- **TLS:** Caddy issues certificates automatically.
- **Domain:** a free subdomain (DuckDNS or similar).
- **Images:** built in GitHub Actions and pushed to GHCR, which is free for public repos.
- **Backups:** nightly `pg_dump` to OCI Object Storage, inside the free 20 GB, with a documented restore drill.

## Sharing the account with MilkRun
The A1 allowance is per account: 3,000 OCPU-hours and 18,000 GB-hours a month, which works out to 4 OCPU / 24 GB running continuously. Oracle allows one free account per person.

**What MilkRun uses (measured 2026-09-27):**
- It runs on the separate AMD Micro allowance (`VM.Standard.E2.1.Micro`: 1/8 OCPU guaranteed, 1 GB RAM), not on A1.
- It needs about 2 GB of RAM to run without swapping, and very little CPU.
- **The A1 allowance is currently entirely unused.**

**Allocation:**

| VM | Shape | Purpose |
|---|---|---|
| MilkRun (if moved to A1, as recommended in its own notes) | 1 OCPU / 6 GB | MilkRun |
| Whiteboard | 3 OCPU / 18 GB | Caddy, 3 nodes, Postgres, Prometheus, Grafana |
| **Total** | **4 OCPU / 24 GB** | exactly the free limit |

If MilkRun ends up needing 2 OCPU / 12 GB, the whiteboard VM shrinks to 2 OCPU / 12 GB. That is still enough for the demo: 2 nodes, Postgres, Prometheus, and Grafana.

**Load tests do not run on Oracle.** They run on GitHub Actions (see BENCHMARKS.md), so they never compete with the demo or MilkRun for the free allowance.

## Free-only risk handling
- **A1 capacity unavailable in the home region:** keep retrying the create (a common, known workaround). Meanwhile, run a reduced demo on the second free AMD Micro VM, which Oracle allows alongside MilkRun's: Postgres with small buffers, Caddy, and no Prometheus or Grafana. (Planned with 1 node; as deployed it runs both, because Caddy depends on them, and an idle node costs about 10 MB.) The public metrics panel still works, because it reads from the node itself.
- **Idle reclamation:** Always Free A1 instances can be reclaimed if their CPU, network, and memory use all stay low for 7 days. We stay on the free account (no Pay-As-You-Go upgrade, so nothing can be billed). Mitigation: everything is infrastructure as code with nightly backups. A reclaimed VM is rebuilt with `terraform apply` plus a restore from backup, and the restore drill is timed and documented.
- **Out of free resources:** cut, in this order:
  1. Grafana (use Prometheus's own UI instead)
  2. the third node (2 nodes still demonstrate failover)
  3. how long Prometheus keeps metrics

## Consequences
- **Self-hosting:** the production stack and the self-host stack are the same Compose file, so self-hosting is `docker compose up`.
- **Latency depends on location:** the demo runs in one region, so users far from it see higher latency. Benchmarks report latency on the benchmark rig separately from end-user (WAN) latency.
- **Downtime risk:** a reclaim or capacity problem can take the demo down for however long a rebuild takes. That rebuild time is measured.

## Revision 2026-10-02: the account's real allowance
At deploy time, Oracle's limits API reported this account's Always Free A1 allowance as **2 OCPU / 12 GB**, not 4 / 24. That is what the "if MilkRun needs more" row above already sized for. With the user's agreement:
- The whiteboard VM takes all of it, 2 OCPU / 12 GB, and runs the full stack: Caddy, 2 nodes, Postgres, Prometheus and Grafana.
- MilkRun stays on its AMD Micro VM, which is a separate allowance. Moving MilkRun to A1 later would mean shrinking this VM.
- The domain is `<ip>.sslip.io`, which needs no account.

The first `terraform apply` hit "Out of host capacity" for A1 in eu-amsterdam-1. Everything else (compartment, network, backup bucket) was created on the first try. The A1 create was retried every 3 minutes for about 2.5 hours (47 attempts, 2026-10-02) without success, then stopped, so as not to hammer the API. **The live demo runs on the fallback Micro VM** (`fallback_micro = true`). Moving to A1 later is one `terraform apply` with `fallback_micro = false`, then a backup and restore ([DEPLOY.md](../DEPLOY.md)).
