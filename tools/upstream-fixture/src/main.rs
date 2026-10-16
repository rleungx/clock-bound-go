use clock_bound::shm::{
    ClockErrorBound, ClockErrorBoundGeneric, ClockErrorBoundLayoutVersion, ClockErrorBoundV3,
    ClockStatus, ShmWrite, ShmWriter,
};
use nix::sys::time::TimeSpec;
use std::path::Path;
fn main() {
    let output = std::env::args().nth(1).expect("pass output .bin path");
    // Start with a new segment so the generation is reproducibly 2.
    match std::fs::remove_file(&output) {
        Ok(()) => (),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => (),
        Err(error) => panic!("cannot replace fixture: {error}"),
    }
    let ceb = ClockErrorBoundGeneric::builder()
        .as_of_tsc(1000)
        .as_of(TimeSpec::new(1_700_000_000, 123_456_789))
        .void_after(TimeSpec::new(1_700_000_120, 123_456_789))
        .period(1.0 / 1024.0)
        .period_err(1.0 / 1_048_576.0)
        .bound_nsec(37)
        .disruption_marker(42)
        .max_drift_ppb(100)
        .clock_status(ClockStatus::Synchronized)
        .clock_disruption_support_enabled(false)
        .build(ClockErrorBoundLayoutVersion::V3);
    let mut writer = ShmWriter::new(
        Path::new(&output),
        ClockErrorBoundLayoutVersion::V3,
        ClockErrorBoundLayoutVersion::V3,
    )
    .unwrap();
    writer.write(&ceb);
    println!(
        "enum={}, V3={} snapshot={:?}",
        std::mem::size_of::<ClockErrorBound>(),
        std::mem::size_of::<ClockErrorBoundV3>(),
        ceb
    );
}
