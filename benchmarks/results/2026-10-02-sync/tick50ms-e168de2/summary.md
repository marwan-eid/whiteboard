| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1000 | 1 | 49.2 | 80.5 | 184.8 | 333.6 | 654.8 | 74939960 | 338182 | 87% | 7% | 487.2MiB | 0 | 51% | 7493MiB | AMD EPYC 7763 64-Core Processor |  |
| 1000 | 2 | 38.4 | 60.2 | 99.9 | 185.3 | 341.8 | 65400020 | 333373 | 53% | 4% | 470.6MiB | 0 | 32% | 4932MiB | AMD EPYC 9V45 96-Core Processor |  |
| 1000 | 3 | 42.8 | 66.6 | 122.8 | 222.8 | 407.3 | 75718459 | 337395 | 65% | 4% | 419.1MiB | 0 | 42% | 6173MiB | AMD EPYC 9V45 96-Core Processor |  |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 1000 | 3 | 122.8 | 99.9, 122.8, 184.8 |
