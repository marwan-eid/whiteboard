## 20ms for very busy boards

| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | Commit p99 | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 500 | 1 | 15.9 | 25.2 | 43.2 | 90.4 | 193.8 | 18873679 | 167612 | 60% | 5% | 244.4MiB | 0 | 26% | 1652MiB | ≤ 6.4 ms | AMD EPYC 7763 64-Core Processor |  |
| 500 | 2 | 14.6 | 23.2 | 36.5 | 97.4 | 230.8 | 17989399 | 166440 | 46% | 5% | 274.3MiB | 0 | 20% | 1567MiB | ≤ 6.4 ms | AMD EPYC 9V45 96-Core Processor |  |
| 500 | 3 | 15.3 | 24.0 | 40.0 | 89.1 | 159.5 | 18337447 | 167649 | 45% | 4% | 257.5MiB | 0 | 22% | 1685MiB | ≤ 3.2 ms | INTEL(R) XEON(R) PLATINUM 8573C |  |
| 500 | 4 | 16.4 | 25.7 | 45.9 | 87.2 | 140.4 | 20034606 | 167766 | 62% | 6% | 238.1MiB | 0 | 28% | 1811MiB | ≤ 6.4 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 1 | 32.1 | 83.3 | 222.3 | 476.7 | 1056.8 | 74686219 | 338889 | 123% | 9% | 459.2MiB | 0 | 65% | 9593MiB | ≤ 25.6 ms | AMD EPYC 7763 64-Core Processor |  |
| 1000 | 2 | 26.5 | 65.2 | 179.8 | 358.7 | 901.6 | 66528138 | 337374 | 108% | 6% | 375.7MiB | 0 | 59% | 7733MiB | ≤ 25.6 ms | Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz |  |
| 1000 | 3 | 32.8 | 84.4 | 213.2 | 418.6 | 1004.5 | 74003823 | 332570 | 125% | 7% | 402.7MiB | 0 | 66% | 9328MiB | ≤ 25.6 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 4 | 33.4 | 87.2 | 214.7 | 394.2 | 1153.0 | 80784436 | 335976 | 127% | 8% | 412.4MiB | 0 | 66% | 9537MiB | ≤ 25.6 ms | AMD EPYC 7763 64-Core Processor |  |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 500 | 4 | 43.2 | 36.5, 40.0, 43.2, 45.9 |
| 1000 | 4 | 214.7 | 179.8, 213.2, 214.7, 222.3 |

## 50ms for very busy boards

| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | Commit p99 | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 500 | 1 | 20.5 | 32.3 | 46.1 | 82.4 | 165.1 | 18365312 | 166711 | 53% | 5% | 292MiB | 0 | 23% | 1662MiB | ≤ 6.4 ms | AMD EPYC 7763 64-Core Processor |  |
| 500 | 2 | 18.9 | 30.4 | 39.1 | 80.7 | 151.2 | 18111158 | 169728 | 39% | 4% | 251MiB | 0 | 18% | 1585MiB | ≤ 12.8 ms | AMD EPYC 9V45 96-Core Processor |  |
| 500 | 3 | 19.8 | 31.5 | 46.8 | 100.5 | 212.5 | 18383764 | 168167 | 39% | 3% | 278.5MiB | 0 | 20% | 1626MiB | ≤ 3.2 ms | INTEL(R) XEON(R) PLATINUM 8573C |  |
| 500 | 4 | 21.1 | 33.2 | 50.4 | 100.3 | 184.7 | 19994480 | 167370 | 53% | 6% | 246.8MiB | 0 | 25% | 1851MiB | ≤ 6.4 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 1 | 48.2 | 78.4 | 178.6 | 341.0 | 688.1 | 73234862 | 332746 | 88% | 7% | 477MiB | 0 | 52% | 6869MiB | ≤ 25.6 ms | AMD EPYC 7763 64-Core Processor |  |
| 1000 | 2 | 45.8 | 73.5 | 167.8 | 323.6 | 604.7 | 66216194 | 335317 | 73% | 5% | 398.4MiB | 0 | 47% | 6332MiB | ≤ 12.8 ms | Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz |  |
| 1000 | 3 | 48.3 | 79.4 | 191.5 | 361.7 | 977.9 | 74683481 | 334119 | 85% | 6% | 406.6MiB | 0 | 52% | 7289MiB | ≤ 25.6 ms | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 4 | 49.4 | 81.9 | 189.4 | 340.2 | 924.2 | 80487762 | 336265 | 89% | 6% | 394.6MiB | 0 | 53% | 8983MiB | ≤ 25.6 ms | AMD EPYC 7763 64-Core Processor |  |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 500 | 4 | 46.8 | 39.1, 46.1, 46.8, 50.4 |
| 1000 | 4 | 189.4 | 167.8, 178.6, 189.4, 191.5 |

