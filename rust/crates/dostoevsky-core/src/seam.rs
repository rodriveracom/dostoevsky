//! The test seam: an injected clock and an injected random source.
//!
//! Unset, the engine reads the system clock and the system's random source, as it must in production. Set, both are
//! fixed, so that two runs of an engine (and a run of each engine) write equal stores. Mirrors `internal/seam` of the
//! Go engine, whose package comment is the contract:
//!
//! - **Clock:** the k-th clock reading returns `start + k * step` nanoseconds.
//! - **Random bytes:** SHA-256(seed || counter) blocks; each request consumes whole blocks.
//! - **Same moments, same count:** the engine reads the clock and the random source at the same points in each
//!   operation, and the same number of times, as the Go engine. A different number of readings shifts every later
//!   value. Each reading site in this workspace names the Go line it mirrors.
//!
//! This file was written before the Go package and does not yet agree with it: the counter below is big-endian and
//! the seed is a byte string, where Go uses a little-endian counter and a 64-bit little-endian seed. See
//! `rust/PORT-NOTES.md`. The first reading is k = 0 ([`now_nanos`]), and [`advance`] moves the clock without
//! counting as a reading, as in Go.

use crate::sha256;
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

struct Fixed {
    start: i64,
    step: i64,
    readings: u64,
    seed: Vec<u8>,
    counter: u64,
}

static FIXED: Mutex<Option<Fixed>> = Mutex::new(None);

fn fixed() -> std::sync::MutexGuard<'static, Option<Fixed>> {
    FIXED.lock().unwrap_or_else(|e| e.into_inner())
}

/// Fixes the clock and the random source for this process. Counting starts again from zero.
pub fn fix(start: i64, step: i64, seed: &[u8]) {
    *fixed() = Some(Fixed {
        start,
        step,
        readings: 0,
        seed: seed.to_vec(),
        counter: 0,
    });
}

/// Back to the system clock and the system's random source.
pub fn unset() {
    *fixed() = None;
}

pub fn is_set() -> bool {
    fixed().is_some()
}

/// Moves a fixed clock forward by `nanoseconds`. Does nothing when the seam is unset.
pub fn advance(nanoseconds: i64) {
    if let Some(f) = fixed().as_mut() {
        f.start = f.start.wrapping_add(nanoseconds);
    }
}

/// Nanoseconds since the Unix epoch.
pub fn now_nanos() -> i64 {
    if let Some(f) = fixed().as_mut() {
        let k = f.readings;
        f.readings += 1;
        return f.start.wrapping_add(f.step.wrapping_mul(k as i64));
    }
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos() as i64)
}

fn block(seed: &[u8], counter: u64) -> [u8; 32] {
    let mut input = Vec::with_capacity(seed.len() + 8);
    input.extend_from_slice(seed);
    input.extend_from_slice(&counter.to_be_bytes());
    sha256::digest(&input)
}

/// Fills `out` with random bytes. Fixed, a request takes whole 32-byte blocks: what it does not use of its last
/// block is discarded, so the next request starts on a block boundary.
pub fn random_bytes(out: &mut [u8]) -> std::io::Result<()> {
    if let Some(f) = fixed().as_mut() {
        for chunk in out.chunks_mut(32) {
            chunk.copy_from_slice(&block(&f.seed, f.counter)[..chunk.len()]);
            f.counter += 1;
        }
        return Ok(());
    }
    system_random(out)
}

#[cfg(unix)]
fn system_random(out: &mut [u8]) -> std::io::Result<()> {
    use std::io::Read;
    std::fs::File::open("/dev/urandom")?.read_exact(out)
}

#[cfg(not(unix))]
fn system_random(_out: &mut [u8]) -> std::io::Result<()> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "no system random source on this platform yet",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    // The seam is one per process, so its tests run as one.
    #[test]
    fn the_contract() {
        unset();
        let before = now_nanos();
        assert!(before > 1_700_000_000_000_000_000 && now_nanos() >= before);
        let (mut a, mut b) = ([0u8; 16], [0u8; 16]);
        random_bytes(&mut a).unwrap();
        random_bytes(&mut b).unwrap();
        assert_ne!(a, b);
        advance(5); // nothing to move

        fix(1_000, 10, b"seed");
        assert!(is_set());
        assert_eq!(
            [now_nanos(), now_nanos(), now_nanos()],
            [1_000, 1_010, 1_020]
        );
        advance(500);
        assert_eq!(now_nanos(), 1_530);

        // Blocks computed with Python: hashlib.sha256(b"seed" + counter.to_bytes(8, "big")).
        let want = include_str!("../../../testdata/seam-random.txt");
        let lines: Vec<&str> = want.lines().filter(|l| !l.starts_with('#')).collect();
        let mut five = [0u8; 5];
        random_bytes(&mut five).unwrap(); // block 0, rest discarded
        let mut forty = [0u8; 40];
        random_bytes(&mut forty).unwrap(); // blocks 1 and 2, rest of block 2 discarded
        let mut whole = [0u8; 32];
        random_bytes(&mut whole).unwrap(); // block 3
        assert_eq!(sha256::hex(&five), lines[0][..10]);
        assert_eq!(
            sha256::hex(&forty),
            format!("{}{}", lines[1], &lines[2][..16])
        );
        assert_eq!(sha256::hex(&whole), lines[3]);

        fix(1_000, 10, b"seed");
        assert_eq!(now_nanos(), 1_000, "fixing again starts the count again");
        let mut again = [0u8; 5];
        random_bytes(&mut again).unwrap();
        assert_eq!(again, five);
        unset();
        assert!(!is_set());
    }
}
