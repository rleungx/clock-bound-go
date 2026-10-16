`upstream-v3.bin` was written by the unmodified AWS `ShmWriter` at commit
`526b8a89d2a8b8ac461a3ab61cadc8c93d48a479` (ClockBound 3.0.0-beta.0).
It is 104 bytes, produced on little-endian arm64 using Rust 1.99.0.

Regenerate from this repository root with:

```sh
cargo run --manifest-path tools/upstream-fixture/Cargo.toml -- testdata/upstream-v3.bin
```

The generator invokes the actual upstream writer with distinct field values,
so the Go ABI tests do not merely round-trip a Go encoder and decoder.
Normal Go tests use the checked-in fixture and require neither Rust nor network.


`rust-differential.jsonl` records 835 outputs of the actual private Rust
projection method, including 831 comparable inputs and four policy rejections.
`rust-reader-traces.json` records 21 steps through the actual Rust public client
and shared-memory readers across six stateful scenarios.

Regenerate those with:

```sh
python3 tools/rust-differential.py --upstream /path/to/clean/pinned/aws/clock-bound
```

The script validates the pinned revision and original source hash, appends a
test-only wrapper in an isolated source copy, and injects only the hardware
counter input for client traces. Projection/reader algorithms are not rewritten.
The output is checked in so ordinary Go tests remain offline and Rust-free.
See `../ALIGNMENT.md` for acceptance rules and limits.
