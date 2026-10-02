| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1000 | 1 | 33.7 | 84.2 | 227.8 | 363.0 | 524.8 | 74398811 | 335489 | 76% | 4% | 459.4MiB | 0 | 44% | 6413MiB | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 2 | 35.5 | 65.2 | 164.1 | 307.5 | 666.6 | 65870709 | 334326 | 103% | 7% | 420.8MiB | 0 | 54% | 6543MiB | AMD EPYC 7763 64-Core Processor |  |
| 1000 | 3 | 37.7 | 79.0 | 208.0 | 422.1 | 1029.6 | 75296176 | 336027 | 107% | 7% | 401.6MiB | 0 | 58% | 8778MiB | AMD EPYC 7763 64-Core Processor |  |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 1000 | 3 | 208.0 | 164.1, 208.0, 227.8 |
