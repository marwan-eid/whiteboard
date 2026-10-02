| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | Commit p99 | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 500 | 1 | 18.2 | 29.6 | 36.7 | 83.4 | 136.8 | 18337799 | 166218 | 32% | 3% | 296.4MiB | 0 | 15% | 1599MiB | ≤ 6.4 ms | AMD EPYC 9V45 96-Core Processor |  |
| 500 | 2 | 23.7 | 82.6 | 234.4 | 437.5 | 652.8 | 17897451 | 165601 | 45% | 4% | 308.2MiB | 0 | 21% | 1556MiB | ≤ 204.8 ms | AMD EPYC 9V74 80-Core Processor |  |
| 500 | 3 | 21.1 | 37.8 | 213.4 | 497.7 | 628.7 | 18172461 | 168079 | 41% | 4% | 232.1MiB | 0 | 19% | 1615MiB | ≤ 102.4 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 1 | 51.8 | 135.4 | 294.4 | 470.3 | 652.3 | 74150946 | 334868 | 56% | 4% | 480.1MiB | 0 | 40% | 5953MiB | ≤ 204.8 ms | Intel(R) Xeon(R) 6973P-C |  |
| 1000 | 2 | 47.4 | 75.8 | 176.0 | 318.5 | 633.3 | 66064477 | 335935 | 84% | 6% | 411.1MiB | 0 | 49% | 6013MiB | ≤ 25.6 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 3 | 47.9 | 130.6 | 393.0 | 606.2 | 832.0 | 75021913 | 337224 | 60% | 4% | 483.3MiB | 0 | 38% | 5612MiB | ≤ 204.8 ms | AMD EPYC 9V45 96-Core Processor |  |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 500 | 3 | 213.4 | 36.7, 213.4, 234.4 |
| 1000 | 3 | 294.4 | 176.0, 294.4, 393.0 |
