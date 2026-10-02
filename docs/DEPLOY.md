# Deploying and self-hosting

There are two ways to run the whiteboard:
1. **Self-host:** any machine with Docker. One command, plain HTTP or your own domain.
2. **The public demo:** Oracle Cloud Always Free, built with Terraform ([ADR-0006](decisions/0006-hosting.md)). It adds TLS, monitoring and backups.

Both run the same [compose.yaml](../compose.yaml). Production adds [deploy/compose.prod.yaml](../deploy/compose.prod.yaml) on top.

## Self-host

```sh
git clone https://github.com/marwan-eid/whiteboard.git && cd whiteboard
SECRET=$(openssl rand -hex 32) docker compose up -d --build
```

Open http://localhost:8080. That runs Caddy, two nodes and Postgres.

To run the production setup on your own server instead (HTTPS, Prometheus, Grafana, nightly backups, published images):
1. Copy [deploy/.env.example](../deploy/.env.example) to `.env` and fill it in. `SITE_ADDRESS` is your domain, which must point at the server.
2. Start it:

   ```sh
   docker compose -f compose.yaml -f deploy/compose.prod.yaml up -d
   ```

Ports 80 and 443 must be open. Caddy gets the certificate by itself.

## The public demo on Oracle Cloud (free)

Everything is in [deploy/terraform](../deploy/terraform). It creates its own compartment, `whiteboard`, so it cannot touch anything else in the account. Inside it:
- **Network:** a VCN and subnet that admit SSH (from `admin_cidr`), HTTP and HTTPS.
- **VM:** one Ampere A1 VM (2 OCPU / 12 GB by default, the demo account's whole free A1 allowance; 100 GB disk) running Ubuntu 24.04.
- **Backups:** a private bucket that deletes backups after 14 days, and a write-only upload URL for the VM.

Within the Always Free limits this costs $0. Do not upgrade the account to Pay-As-You-Go: on a free account nothing can be billed.

**Steps:**
1. **API key:** in the OCI console, open your user → *API keys* → *Add API key* and upload a public key, for example `openssl genrsa -out ~/.oci/whiteboard_api_key.pem 2048 && openssl rsa -pubout -in ~/.oci/whiteboard_api_key.pem`. Note the tenancy OCID, user OCID, fingerprint and home region that the console shows.
2. **SSH key:** `ssh-keygen -t ed25519 -f ~/.ssh/whiteboard_oci`.
3. **Variables:** copy [terraform.tfvars.example](../deploy/terraform/terraform.tfvars.example) to `terraform.tfvars` and fill it in. The domain can be left empty: the demo then gets `<ip-with-dashes>.sslip.io`, a hostname that needs no account. Or give a DuckDNS name and its token.
4. **Create it:**

   ```sh
   cd deploy/terraform
   terraform init
   terraform apply
   ```

   If A1 capacity is unavailable (`Out of host capacity`), run `terraform apply` again later. This is common in busy regions.
5. **Wait about 5 minutes.** First boot installs Docker, pulls the images from GHCR and starts the stack. Progress is in `/var/log/whiteboard-setup.log` on the VM. Then open the `url` output.

**What runs:**

| Service | Exposed? | Notes |
|---|---|---|
| Caddy (`web`) | yes: 80, 443 | TLS, the app, routing to nodes |
| `node-1`, `node-2` | no | images `ghcr.io/marwan-eid/whiteboard-node`, published by [release.yml](../.github/workflows/release.yml) after CI passes |
| Postgres 17 | no | |
| Prometheus | no | scrapes both nodes every 15 s, keeps 15 days |
| Grafana | at `/grafana/`, read-only for visitors | admin password: `terraform output -raw grafana_admin_password` |
| `backup` | no | `pg_dump` nightly at 03:00 UTC to Object Storage; the last 7 are kept on the VM |

**Updating:** `ssh ubuntu@<ip>`, then `cd /opt/whiteboard && sh deploy/update.sh`. It pulls the repo and the images, then restarts the nodes one at a time; clients of the restarting node move to the other one. Pass a commit SHA to pin or roll back: `sh deploy/update.sh <sha>`.

## Backups and restore

`deploy/backup/backup.sh` writes a `pg_dump` (custom format) every night. Upload is through a pre-authenticated URL that can write but cannot read or list, so the VM holds no account credentials.

Back up now:

```sh
docker compose -f compose.yaml -f deploy/compose.prod.yaml exec backup sh /backup/backup.sh now
```

**Restore:**
1. **Get the dump.** Restoring from Object Storage needs a URL that can read the object: create a pre-authenticated request for that one object in the console (*Bucket → Objects → ⋮ → Create Pre-Authenticated Request*, read-only). Or copy a local dump from the `backups` volume.
2. **Restore it:**

   ```sh
   C="docker compose -f compose.yaml -f deploy/compose.prod.yaml"
   $C stop node-1 node-2
   $C run --rm --entrypoint sh backup /backup/restore.sh <dump file or URL>
   $C start node-1 node-2
   ```

   The restore replaces the database inside one transaction.

**Restore drill (verified):**
- **Check:** [cmd/boardcheck](../cmd/boardcheck) prints a board's seq, object count and a SHA-256 of every object's state. A restore is correct when that line is identical before and after.
- **Local run, 2026-10-02:** the full production stack on Docker Desktop.
  1. A board was built with 20,000 seeded objects and 50 simulated editors.
  2. Fingerprint: `seq 844, 20267 objects, sha256 320998bd…`.
  3. Backup: 411 KB, written in under a second.
  4. The whole stack was destroyed, volumes included (`down -v`).
  5. A fresh stack took 15 s to come up; the restore, including stopping and starting the nodes, took 5 s.
  6. The fingerprint afterwards was identical.
- **On the live server, 2026-10-02:** the fallback Micro VM, restoring from Object Storage.
  1. Fingerprinted two boards: `drill` (5,000 objects, seq 10) and a WAN-test board (725 objects, seq 3,619).
  2. Backup: 221 KB, written in about 1 s and uploaded to the bucket through the write-only link.
  3. Wiped the stack with `down -v`, after keeping a copy of the dump outside the volumes.
  4. A fresh stack took 32 s.
  5. Restored from the bucket through a temporary read-only link: 2 s for the restore, 12 s including stopping and starting the nodes (18 boards, 3,769 log rows).
  6. Both fingerprints afterwards were identical.

**Rebuilding after a reclaim** (Oracle may reclaim idle free VMs): `terraform apply`, which makes a new VM and a new IP. Then restore the latest backup from the bucket as above. With a DuckDNS name the address is kept; with sslip.io it changes along with the IP.
