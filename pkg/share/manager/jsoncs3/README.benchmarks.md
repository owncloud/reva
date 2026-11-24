# Benchmarks of the jsoncs3 share manager

The benchmarks in `bench_test.go` measure the share manager with a counting
storage. They compare two commits, for example the code before and after a
change of the caches.

## Files

| File | Content |
|---|---|
| `bench_test.go` | the scenarios. They use only the `share.Manager` methods |
| `bench_shim_test.go` | the constructor and the fields of `jsoncs3.Manager`. If a commit changes them, change only this file |
| `bench_fakes_test.go` | `casStorage`, `countingStorage` and `countingStream` |
| `bench_casstorage_test.go`, `bench_storage_test.go` | tests of the fakes |

`casStorage` is an in-memory storage with a correct CAS check. It checks the
conditions and writes in one step. It refuses an If-Match on a missing file,
and it gives each write a new etag. `metadata.Disk` does neither, so it cannot
prove that no change gets lost.

`countingStorage` counts the calls by result. It waits a fixed time for each
call, because the in-memory storage has no network cost. Without the wait, a
lock convoy does not show.

Each operation does a fixed amount of work, and each iteration checks its
result. Thus a faster implementation does not get more work, and a wrong
implementation cannot look fast.

## Scenarios

| Benchmark | What it measures | Design phase |
|---|---|---|
| `Login/warm`, `/cold` | `ListReceivedShares` of a user in 30 groups with 50 received shares in 10 spaces. `cold` uses a new pod for each listing | 0, 1 to 3 |
| `HotGroup` | 200 members of one group list at the same time on one pod. In each round, 5 writers add and remove 20 shares of the group. The values are for one listing | 1 to 3 |
| `LargeSpace/list-cold`, `/list-warm`, `/update` | one space with 1,000 shares | 3, 5 |
| `Pods/pods=2`, `/pods=10` | many pods share and unshare in the same space. A new pod then checks the space document, `created.json` and the received shares. `lost` must be 0 | 1 to 3 |
| `Ghosts/in-grace` | a listing with 20 stale group entries, inside the grace period | 4 |
| `Ghosts/after-grace` | the same after the grace period. Today the listing removes the entries | 4 |
| `Expired` | a listing with 10 valid and 10 expired shares. Today the listing removes the expired shares and publishes the events | 4 |
| `AcceptGroupShare` | one group share, then 50 members accept it, 10 at the same time. With auto-accept, the frontend does this | 2 |
| `ListCreated/warm`, `/cold` | `ListShares` of a user with 100 created shares in 10 spaces | 1, 3 |

## Values

| Unit | Meaning |
|---|---|
| `dl200/op`, `dl304/op`, `dl404/op`, `dlerr/op` | downloads by result |
| `upload/op`, `conflict/op`, `uperr/op` | uploads by result. A conflict is a failed CAS condition |
| `other/op` | `Stat`, `MakeDirIfNotExist`, `ListDir`, `Delete` |
| `calls/op` | all counted storage calls |
| `events/op` | published events, for example `ShareExpired` |
| `B/op`, `allocs/op` | memory allocations |
| `p99-ms/listing` | 99th percentile of one listing (`HotGroup`) |
| `lost` | shares that the check after `Pods` did not find as expected |

A `cold` value includes 3 `other` calls for the start of the pod
(`MakeDirIfNotExist` of `storages`, `users` and `groups`).

The counts include background work that runs during the measured time. A
design that moves work to the background can thus show it later, in the next
iteration, or not at all. Check `events/op` and the counts of `Expired` and
`Ghosts/after-grace` with this in mind.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `JSONCS3_BENCH_RTT` | `1ms` | wait for each storage call. `0` measures only the CPU, for example the JSON decode |
| `JSONCS3_BENCH_MAX_CONCURRENCY` | `10` | `MaxConcurrency` of the managers. The ocis default is 1 |

## Compare two commits

1. Make a worktree for each commit, for example:

   ```bash
   git worktree add ../reva-old <old-commit>
   git worktree add ../reva-new <new-commit>
   ```

2. Copy the `bench_*_test.go` files into the old worktree, if the old commit
   does not have them. If the old commit has a different constructor, change
   `bench_shim_test.go` there.
3. Run the benchmarks in each worktree with the same settings:

   ```bash
   GOWORK=off go test ./pkg/share/manager/jsoncs3/ -run '^$' -bench . -count=10 | tee /tmp/old.txt
   GOWORK=off go test ./pkg/share/manager/jsoncs3/ -run '^$' -bench . -count=10 | tee /tmp/new.txt
   ```

   `Pods` needs more iterations for stable conflict counts. Run it also
   with `-bench Pods -benchtime=200x`.

4. Compare the results:

   ```bash
   go run golang.org/x/perf/cmd/benchstat@latest /tmp/old.txt /tmp/new.txt
   ```

5. For the lock wait, make profiles of `HotGroup` on both commits:

   ```bash
   GOWORK=off go test ./pkg/share/manager/jsoncs3/ -run '^$' -bench HotGroup -mutexprofile /tmp/mutex.out -blockprofile /tmp/block.out
   go tool pprof -top /tmp/block.out
   ```

Rules:

- Use the same Go version for both runs. A `go.work` file can select a
  different toolchain than `go.mod`. `GOWORK=off` prevents this.
- Run the two commits one after the other on an idle machine. Repeat the
  pair if the results vary.
- The fake event stream only counts the events. A manager reads no events in
  the benchmarks.

## Not covered

- The calls inside `metadata.CS3`, for example `Authenticate`. For these, run
  oCIS with tracing to Grafana Tempo and count the spans. The ocis repository
  has a Tempo setup in
  `deployments/examples/ocis_full/monitoring_tracing/tempo.yml`.
- Failed and slow storage calls. A time budget and the context of the
  caller need their own scenario with fault injection.
- The cost of the body transfer. The wait for each call is the same for a
  small document and for a large one.
