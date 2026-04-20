# Kaptanto Benchmark Report

Generated: 2026-04-07T22:44:13Z

[View interactive report](./report.html)

## Executive Summary

| Tool | Peak Throughput (eps) | p50 Latency (ms) | p95 Latency (ms) | Recovery (s) | Infrastructure |
| --- | --- | --- | --- | --- | --- |
| kaptanto | 36267 | 1146.8 | 16863.6 | 4.3 | 1 binary (Go, ~15 MB) |
| kaptanto-rust | 31883 | 992.6 | 6727.2 | 3.1 | 1 binary (Go+Rust FFI, ~15 MB) |
| debezium | 351 | 6004.2 | 7370.6 | 2.7 | JVM + config files |
| sequin | 357 | 1579.2 | 13457.6 | 81.8 | Elixir + Redis + PG |

## Latency (p50 / p95 / p99 ms)

| Tool | steady | burst | large-batch | crash-recovery |
| --- | --- | --- | --- | --- |
| kaptanto | 1146.83/16863.57/19997.15 ms | 2858.37/9822.73/11657.70 ms | 2655.84/6952.65/7391.21 ms | 29850.70/124988.54/140213.25 ms |
| kaptanto-rust | 992.61/6727.21/10062.20 ms | 4562.88/12519.75/14177.18 ms | 2731.04/6928.68/7372.57 ms | 7589.98/34165.59/39436.49 ms |
| debezium | 34617.28/62339.54/64070.99 ms | 7001.27/27506.18/29274.69 ms | 6004.20/7370.57/7457.50 ms | 145059.65/237226.44/242707.19 ms |
| sequin | 23638.01/60133.09/62574.02 ms | 1579.22/13457.65/14338.04 ms | 5033.97/7304.84/7464.47 ms | 172152.56/242202.39/245573.32 ms |

## Throughput

| Tool | steady | burst | large-batch | crash-recovery |
| --- | --- | --- | --- | --- |
| kaptanto | 4805 eps | 7141 eps | 36267 eps | 2594 eps |
| kaptanto-rust | 3559 eps | 6061 eps | 31883 eps | 1394 eps |
| debezium | 128 eps | 351 eps | 150 eps | 205 eps |
| sequin | 220 eps | 357 eps | 324 eps | 86 eps |

## RSS Memory

| Tool | steady | burst | large-batch | crash-recovery |
| --- | --- | --- | --- | --- |
| kaptanto | 1112.1 MB | 882.7 MB | 746.0 MB | 1740.2 MB |
| kaptanto-rust | 1270.2 MB | 793.2 MB | 582.4 MB | 1426.3 MB |
| debezium | 365.2 MB | 360.1 MB | 272.5 MB | 468.8 MB |
| sequin | 774.6 MB | 760.6 MB | 672.6 MB | 797.7 MB |

## Recovery Time

| Tool | Recovery (s) |
| --- | --- |
| kaptanto | 4.26 s |
| kaptanto-rust | 3.07 s |
| debezium | 2.72 s |
| sequin | 81.84 s |

