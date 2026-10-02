| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | CPU | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1000 | 1 | 36.5 | 91.7 | 231.4 | 420.4 | 987.1 | 73564437 | 333453 | 128% | 8% | 451.3MiB | 0 | 67% | 9516MiB | AMD EPYC 7763 64-Core Processor |  |
| 1000 | 2 | 25.7 | 105.9 | 295.7 | 424.4 | 534.5 | 65969009 | 336511 | 96% | 6% | 406.9MiB | 0 | 49% | 6442MiB | AMD EPYC 9V74 80-Core Processor |  |
| 1000 | 3 | 37.2 | 93.0 | 212.1 | 385.3 | 837.1 | 74838664 | 333123 | 132% | 8% | 406.4MiB | 0 | 66% | 9598MiB | AMD EPYC 7763 64-Core Processor |  |
| 1500 | 1 | 141.4 | 650.2 | 2273.3 | 4304.9 | 8003.6 | 159390623 | 498771 | 140% | 7% | 591.3MiB | 0 | 88% | 10391MiB | INTEL(R) XEON(R) PLATINUM 8573C | invalid: loadgen CPU over 70% |
| 1500 | 2 | 114.2 | 434.9 | 1258.5 | 2494.5 | 5070.8 | 158109146 | 500767 | 105% | 5% | 550.2MiB | 0 | 75% | 10338MiB | Intel(R) Xeon(R) 6973P-C | invalid: loadgen CPU over 70% |
| 1500 | 3 | 277.0 | 1012.2 | 3385.3 | 5849.1 | 10567.7 | 175416082 | 487662 | 152% | 10% | 609.7MiB | 0 | 90% | 10385MiB | AMD EPYC 7763 64-Core Processor | invalid: loadgen CPU over 70% |

| Editors | Valid runs | Median p99 ms | p99 of each valid run |
|---|---|---|---|
| 1000 | 3 | 231.4 | 212.1, 231.4, 295.7 |
| 1500 | 0 | – |  |
