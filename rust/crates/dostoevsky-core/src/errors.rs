//! The errors a caller can tell apart. Mirrors the sentinel errors of the Go engine (`Err...` in `node.go` and its
//! neighbours) and the code names the conformance driver reports for them.
//!
//! A Go error can wrap more than one sentinel, and the Go driver reports the first match in a fixed order. So an
//! [`Error`] carries every kind it wraps, and [`Error::code`] applies the same order.

use std::fmt;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Kind {
    // The order is the order the Go driver tests in: an error that wraps another comes first.
    JournalFull,
    Limit,
    Invalid,
    Owner,
    Closed,
    Conflict,
    Expired,
    Empty,
    CorruptLog,
    IncompleteBackup,
    BackupNotDurable,
    UpgradeRequired,
    SegmentedJournal,
    LaterFrames,
    NotRepairable,
    /// Go's `fs.ErrExist`.
    Exists,
}

impl Kind {
    pub const ALL: [Kind; 16] = [
        Kind::JournalFull,
        Kind::Limit,
        Kind::Invalid,
        Kind::Owner,
        Kind::Closed,
        Kind::Conflict,
        Kind::Expired,
        Kind::Empty,
        Kind::CorruptLog,
        Kind::IncompleteBackup,
        Kind::BackupNotDurable,
        Kind::UpgradeRequired,
        Kind::SegmentedJournal,
        Kind::LaterFrames,
        Kind::NotRepairable,
        Kind::Exists,
    ];

    pub fn code(self) -> &'static str {
        match self {
            Kind::JournalFull => "journal_full",
            Kind::Limit => "limit",
            Kind::Invalid => "invalid",
            Kind::Owner => "owner",
            Kind::Closed => "closed",
            Kind::Conflict => "conflict",
            Kind::Expired => "expired",
            Kind::Empty => "empty",
            Kind::CorruptLog => "corrupt_log",
            Kind::IncompleteBackup => "incomplete_backup",
            Kind::BackupNotDurable => "backup_not_durable",
            Kind::UpgradeRequired => "upgrade_required",
            Kind::SegmentedJournal => "segmented_journal",
            Kind::LaterFrames => "later_frames",
            Kind::NotRepairable => "not_repairable",
            Kind::Exists => "exists",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error {
    kinds: Vec<Kind>,
    message: String,
}

impl Error {
    /// An error with no kind a caller can test for. The driver reports it as `other`.
    pub fn other(message: impl Into<String>) -> Error {
        Error {
            kinds: Vec::new(),
            message: message.into(),
        }
    }

    pub fn new(kind: Kind, message: impl Into<String>) -> Error {
        Error {
            kinds: vec![kind],
            message: message.into(),
        }
    }

    /// The same error, also wrapping `kind`.
    pub fn also(mut self, kind: Kind) -> Error {
        if !self.kinds.contains(&kind) {
            self.kinds.push(kind);
        }
        self
    }

    /// Go's `errors.Is` against one sentinel.
    pub fn is(&self, kind: Kind) -> bool {
        self.kinds.contains(&kind)
    }

    /// The name the conformance driver reports.
    pub fn code(&self) -> &'static str {
        Kind::ALL
            .iter()
            .find(|k| self.is(**k))
            .map_or("other", |k| k.code())
    }

    pub fn message(&self) -> &str {
        &self.message
    }

    /// What every engine operation returns until its row is ported.
    pub fn not_ported(operation: &str, row: u8) -> Error {
        Error::other(format!(
            "dostoevsky: {operation} is not ported yet (row {row})"
        ))
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for Error {}

pub type Result<T> = std::result::Result<T, Error>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_first_kind_in_the_driver_order_is_reported() {
        assert_eq!(Error::other("x").code(), "other");
        assert_eq!(
            Error::new(Kind::Invalid, "x").also(Kind::Limit).code(),
            "limit"
        );
        assert_eq!(
            Error::new(Kind::Exists, "x").also(Kind::CorruptLog).code(),
            "corrupt_log"
        );
        let mut sorted = Kind::ALL;
        sorted.sort();
        assert_eq!(
            sorted,
            Kind::ALL,
            "ALL must list the kinds in declaration order"
        );
    }
}
