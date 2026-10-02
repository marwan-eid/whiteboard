# Launch: where to share it

Check each community's self-promotion rules before posting, and post from your own account. Lead with what is technically interesting (the sync model, viewport interest, the honest load-test results), not with "I built a whiteboard".

## Places

| Where | What to post | Notes |
|---|---|---|
| Hacker News, "Show HN" | Title like "Show HN: A self-hosted whiteboard that only syncs what you look at". Link the repo; put the demo link and two or three lines of context in the first comment. | Weekday mornings US time are usual. Answer questions in the thread. |
| r/golang | The Go side: one goroutine per board, encode-once fan-out, the lease and fencing story, the profiling results. | The subreddit is fine with project posts that explain something. |
| r/selfhosted | `docker compose up`, the self-hosting guide, $0 hosting on Oracle Free. | Show the Compose file and the backup and restore story. |
| r/webdev or r/javascript | The client: PixiJS rendering with level of detail, IndexedDB offline queue, Web Locks per tab. | |
| dev.to, Hashnode or your blog | [WRITEUP.md](WRITEUP.md), lightly edited. | Cross-link from the README. |
| LinkedIn | A short post: the problem, the two bugs the randomized tests found, the measured numbers (including the target not reached), and the demo link plus the GIF. | Your network is where recruiters see it. |
| Golang Weekly, JavaScript Weekly | Submit the write-up link through their sites. | Curated, so not guaranteed. |
| Lobsters | Only with an invite, and authors should link write-ups rather than repos. | |
| awesome-selfhosted | Not yet: its rules require the first release to be at least 4 months old. | Revisit later. |

## A short post to adapt

> I built an open-source real-time whiteboard to learn what it takes to sync a large board between many people.
>
> **What's different:**
> - The server decides which part of a board each browser holds, so a 100,000-shape board only downloads what's on screen.
> - Edits are timestamped per property and merge in any order, so offline edits converge.
> - Every version can be replayed and restored, at about 31 bytes per edit.
>
> **Two bugs the randomized tests caught:**
> - A batch that wrote one property twice could leave two clients with different values under the same timestamp. It had been there since the first milestone.
> - A paused server node kept committing as a stale owner, and the fencing made the *new* owner's writes fail instead.
>
> **Load test** (free GitHub runners, 2 cores for the server): p99 22 ms at 100 editors and 58 ms at 500. My 1,000-editor target was **not** reached (231 ms); the write-up says why and what I'd try next.
>
> Demo (free Oracle tier): <demo link> · Code: https://github.com/marwan-eid/whiteboard · Write-up: docs/WRITEUP.md

## Before posting

- [ ] Open the demo in two browsers and on a phone: drawing, timer, vote, share link.
- [ ] Check the live stats panel and `/grafana/` (Grafana only on the A1 VM).
- [ ] Confirm last night's backup reached the bucket.
- [ ] Usage counts start at zero; the panel shows them live after launch.
