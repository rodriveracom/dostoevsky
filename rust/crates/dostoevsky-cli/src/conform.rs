//! The conformance driver for this engine. Mirrors `cmd/dost-conform/main.go`: one JSON request per line in, one JSON
//! reply per line out, until end of input. The protocol is described in `cmd/dost-conform/README.md`.
//!
//! Two requests exist only here so far, for the differential test: `fix_inputs` and `advance` set the test seam
//! (`dostoevsky_core::seam`). The Go driver gets the same two; their field names here are provisional until it does.
//!
//! Row 0 has no engine behind it. Every engine operation answers with the error `other` and names the row that
//! brings it. The parts that do not depend on the engine are complete and tested: reading requests as Go's decoder
//! does, the order of the checks, the error codes, and the reply bytes.

use dostoevsky_core::errors::Error;
use dostoevsky_core::seam;
use dostoevsky_format::base64;
use dostoevsky_format::gojson::{self, ParseError, Value};
use std::io::{BufRead, Write};

#[derive(Debug, Default, Clone, PartialEq)]
pub struct Message {
    pub topic: String,
    pub key: String,
    pub value: Option<Vec<u8>>,
    pub id: String,
}

#[derive(Debug, Default, Clone, PartialEq)]
pub struct Floor {
    pub topic: String,
    pub partition: i64,
    pub floor: i64,
}

/// One request. A field the request does not carry keeps its zero value, as in Go.
#[derive(Debug, Default, Clone, PartialEq)]
pub struct Request {
    pub op: String,
    pub dir: String,
    pub segmented: bool,
    pub node: String,
    pub nodes: Option<Vec<String>>,
    pub topics: Option<Vec<(String, i64)>>,
    pub items: Option<Vec<Message>>,
    pub group: String,
    pub offset: i64,
    pub next: i64,
    pub state: Option<Vec<u8>>,
    pub records: i64,
    pub bytes: i64,
    pub floors: Option<Vec<Floor>>,
    pub dest: String,
    pub discard_later: bool,
    pub topic: String,
    pub partition: i64,
    pub key: String,
    pub value: Option<Vec<u8>>,
    pub id: String,
    // The seam requests (provisional names).
    pub start: i64,
    pub step: i64,
    pub seed: Option<Vec<u8>>,
    pub nanoseconds: i64,
}

type Decoded<T> = Result<T, String>;

fn kind(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

fn wrong<T>(field: &str, v: &Value, want: &str) -> Decoded<T> {
    Err(format!(
        "json: cannot unmarshal {} into Go struct field {field} of type {want}",
        kind(v)
    ))
}

// Each setter leaves the field alone for null, as Go does.

fn set_string(to: &mut String, field: &str, v: &Value) -> Decoded<()> {
    match v {
        Value::Null => Ok(()),
        Value::String(s) => {
            *to = s.clone();
            Ok(())
        }
        _ => wrong(field, v, "string"),
    }
}

fn set_bool(to: &mut bool, field: &str, v: &Value) -> Decoded<()> {
    match v {
        Value::Null => Ok(()),
        Value::Bool(b) => {
            *to = *b;
            Ok(())
        }
        _ => wrong(field, v, "bool"),
    }
}

fn set_int(to: &mut i64, field: &str, v: &Value) -> Decoded<()> {
    match v {
        Value::Null => Ok(()),
        // Go accepts only a literal that is an integer as written: no fraction and no exponent.
        Value::Number(n) => match n.parse::<i64>() {
            Ok(i) if !n.starts_with('+') => {
                *to = i;
                Ok(())
            }
            _ => Err(format!(
                "json: cannot unmarshal number {n} into Go struct field {field} of type int"
            )),
        },
        _ => wrong(field, v, "int"),
    }
}

fn set_bytes(to: &mut Option<Vec<u8>>, field: &str, v: &Value) -> Decoded<()> {
    match v {
        Value::Null => {
            *to = None;
            Ok(())
        }
        Value::String(s) => match base64::decode(s) {
            Some(b) => {
                *to = Some(b);
                Ok(())
            }
            None => Err(format!("illegal base64 data in field {field}")),
        },
        _ => wrong(field, v, "[]uint8"),
    }
}

/// Go matches a key to a field by its exact name first and otherwise without regard to case.
fn field_of<'a>(key: &str, names: &[&'a str]) -> Option<&'a str> {
    names
        .iter()
        .find(|n| **n == key)
        .or_else(|| names.iter().find(|n| n.eq_ignore_ascii_case(key)))
        .copied()
}

fn members<'a>(v: &'a Value, what: &str) -> Decoded<&'a [(String, Value)]> {
    match v {
        Value::Object(m) => Ok(m),
        _ => wrong(what, v, "struct"),
    }
}

fn unknown<T>(key: &str) -> Decoded<T> {
    Err(format!("json: unknown field {key:?}"))
}

fn message(v: &Value) -> Decoded<Message> {
    let mut m = Message::default();
    for (key, v) in members(v, "items")? {
        match field_of(key, &["topic", "key", "value", "id"]) {
            Some("topic") => set_string(&mut m.topic, "topic", v)?,
            Some("key") => set_string(&mut m.key, "key", v)?,
            Some("value") => set_bytes(&mut m.value, "value", v)?,
            Some("id") => set_string(&mut m.id, "id", v)?,
            _ => return unknown(key),
        }
    }
    Ok(m)
}

fn floor(v: &Value) -> Decoded<Floor> {
    let mut f = Floor::default();
    for (key, v) in members(v, "floors")? {
        match field_of(key, &["topic", "partition", "floor"]) {
            Some("topic") => set_string(&mut f.topic, "topic", v)?,
            Some("partition") => set_int(&mut f.partition, "partition", v)?,
            Some("floor") => set_int(&mut f.floor, "floor", v)?,
            _ => return unknown(key),
        }
    }
    Ok(f)
}

fn list<T>(
    to: &mut Option<Vec<T>>,
    field: &str,
    v: &Value,
    item: impl Fn(&Value) -> Decoded<T>,
) -> Decoded<()> {
    match v {
        Value::Null => {
            *to = None;
            Ok(())
        }
        Value::Array(items) => {
            *to = Some(items.iter().map(item).collect::<Decoded<Vec<T>>>()?);
            Ok(())
        }
        _ => wrong(field, v, "slice"),
    }
}

const FIELDS: [&str; 25] = [
    "op",
    "dir",
    "segmented",
    "node",
    "nodes",
    "topics",
    "items",
    "group",
    "offset",
    "next",
    "state",
    "records",
    "bytes",
    "floors",
    "dest",
    "discard_later",
    "topic",
    "partition",
    "key",
    "value",
    "id",
    "start",
    "step",
    "seed",
    "nanoseconds",
];

/// Reads a request as Go's decoder reads the driver's request struct with unknown fields disallowed. A key given
/// twice takes its last value.
pub fn decode(v: &Value) -> Decoded<Request> {
    let mut q = Request::default();
    for (key, v) in members(v, "request")? {
        let Some(name) = field_of(key, &FIELDS) else {
            return unknown(key);
        };
        match name {
            "op" => set_string(&mut q.op, name, v)?,
            "dir" => set_string(&mut q.dir, name, v)?,
            "segmented" => set_bool(&mut q.segmented, name, v)?,
            "node" => set_string(&mut q.node, name, v)?,
            "nodes" => list(&mut q.nodes, name, v, |v| {
                let mut s = String::new();
                set_string(&mut s, "nodes", v).map(|_| s)
            })?,
            "topics" => match v {
                Value::Null => q.topics = None,
                Value::Object(m) => {
                    // A Go map: a key given twice keeps one entry, with the last value.
                    let mut topics: Vec<(String, i64)> = q.topics.take().unwrap_or_default();
                    for (topic, count) in m {
                        let mut n = 0;
                        set_int(&mut n, "topics", count)?;
                        match topics.iter_mut().find(|t| t.0 == *topic) {
                            Some(t) => t.1 = n,
                            None => topics.push((topic.clone(), n)),
                        }
                    }
                    q.topics = Some(topics);
                }
                _ => return wrong(name, v, "map[string]int"),
            },
            "items" => list(&mut q.items, name, v, message)?,
            "group" => set_string(&mut q.group, name, v)?,
            "offset" => set_int(&mut q.offset, name, v)?,
            "next" => set_int(&mut q.next, name, v)?,
            "state" => set_bytes(&mut q.state, name, v)?,
            "records" => set_int(&mut q.records, name, v)?,
            "bytes" => set_int(&mut q.bytes, name, v)?,
            "floors" => list(&mut q.floors, name, v, floor)?,
            "dest" => set_string(&mut q.dest, name, v)?,
            "discard_later" => set_bool(&mut q.discard_later, name, v)?,
            "topic" => set_string(&mut q.topic, name, v)?,
            "partition" => set_int(&mut q.partition, name, v)?,
            "key" => set_string(&mut q.key, name, v)?,
            "value" => set_bytes(&mut q.value, name, v)?,
            "id" => set_string(&mut q.id, name, v)?,
            "start" => set_int(&mut q.start, name, v)?,
            "step" => set_int(&mut q.step, name, v)?,
            "seed" => set_bytes(&mut q.seed, name, v)?,
            "nanoseconds" => set_int(&mut q.nanoseconds, name, v)?,
            _ => unreachable!("every name in FIELDS is handled"),
        }
    }
    Ok(q)
}

/// The reply line for a result and an error. A result can come with an error: a refused repair still reports what
/// it found. Field order and omissions are those of the Go driver's reply struct.
pub fn reply(result: Option<Value>, error: Option<&Error>) -> Vec<u8> {
    let mut members = vec![("ok".to_string(), Value::Bool(error.is_none()))];
    if let Some(e) = error {
        members.push(("error".into(), Value::text(e.code())));
        if !e.message().is_empty() {
            members.push(("message".into(), Value::text(e.message())));
        }
    }
    if let Some(r) = result {
        members.push(("result".into(), r));
    }
    gojson::encode_line(&Value::Object(members))
}

/// The driver's state. It will hold the open node from row 6 on.
#[derive(Default)]
pub struct Driver {
    node: Option<std::convert::Infallible>,
}

impl Driver {
    /// Performs one request. The order of the checks is the Go driver's.
    pub fn run(&mut self, q: &Request) -> (Option<Value>, Result<(), Error>) {
        match q.op.as_str() {
            "fix_inputs" => {
                seam::fix(q.start, q.step, q.seed.as_deref().unwrap_or_default());
                return (None, Ok(()));
            }
            "advance" => {
                seam::advance(q.nanoseconds);
                return (None, Ok(()));
            }
            "open" => {
                if self.node.is_some() {
                    return (
                        None,
                        Err(Error::other("dost-conform: a node is already open")),
                    );
                }
                let what = if q.segmented { "OpenSegmented" } else { "Open" };
                return (
                    None,
                    Err(Error::not_ported(what, if q.segmented { 5 } else { 6 })),
                );
            }
            "inspect" => return (None, Err(Error::not_ported("InspectJournal", 8))),
            "repair" => return (None, Err(Error::not_ported("RepairJournal", 8))),
            "upgrade" => return (None, Err(Error::not_ported("UpgradeJournal", 8))),
            _ => {}
        }
        if self.node.is_none() {
            return (None, Err(Error::other("dost-conform: no node is open")));
        }
        let row = match q.op.as_str() {
            "close" | "publish" | "publish_batch" | "read" | "read_batch" | "position"
            | "commit_batch" => 6,
            "retain" => 7,
            "backup" | "migrate" => 8,
            "verify" => 5,
            _ => {
                return (
                    None,
                    Err(Error::other(format!("dost-conform: unknown op {}", q.op))),
                )
            }
        };
        (None, Err(Error::not_ported(&q.op, row)))
    }
}

/// Serves requests until end of input. An error here ends the driver with status 1, as in Go: input that is not
/// JSON, a field the driver does not know, or a value of the wrong type.
pub fn serve(input: &mut impl BufRead, output: &mut impl Write) -> Result<(), String> {
    let mut driver = Driver::default();
    let mut pending: Vec<u8> = Vec::new();
    let mut at_eof = false;
    loop {
        let parsed = match gojson::parse_prefix(&pending, at_eof) {
            Ok(done) => Some(done),
            Err(ParseError::Incomplete) if !at_eof => None,
            Err(ParseError::Incomplete) if pending.iter().all(u8::is_ascii_whitespace) => {
                return Ok(())
            }
            Err(ParseError::Incomplete) => return Err("unexpected EOF".into()),
            Err(e) => return Err(e.to_string()),
        };
        let Some((value, used)) = parsed else {
            at_eof = input
                .read_until(b'\n', &mut pending)
                .map_err(|e| e.to_string())?
                == 0;
            continue;
        };
        pending.drain(..used);
        let request = decode(&value)?;
        let (result, outcome) = driver.run(&request);
        output
            .write_all(&reply(result, outcome.as_ref().err()))
            .and_then(|_| output.flush())
            .map_err(|e| e.to_string())?;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use dostoevsky_core::errors::Kind;

    fn served(input: &str) -> (String, Result<(), String>) {
        let mut out = Vec::new();
        let status = serve(&mut input.as_bytes(), &mut out);
        (String::from_utf8(out).unwrap(), status)
    }

    #[test]
    fn requests_decode_as_in_go() {
        let v = gojson::parse(br#"{"op":"publish","topic":"t","Partition":3,"value":"aGk=","value":"eW8=","key":null,"items":[{"topic":"a","value":null}],"topics":{"a":1,"b":2,"a":4},"nodes":["n1"],"floors":[{"topic":"a","partition":1,"floor":9}]}"#).unwrap();
        let q = decode(&v).unwrap();
        assert_eq!(
            (q.op.as_str(), q.topic.as_str(), q.partition),
            ("publish", "t", 3)
        );
        assert_eq!(
            q.value.as_deref(),
            Some(&b"yo"[..]),
            "the last of a repeated key wins"
        );
        assert_eq!(q.key, "");
        assert_eq!(
            q.items,
            Some(vec![Message {
                topic: "a".into(),
                ..Default::default()
            }])
        );
        assert_eq!(q.topics, Some(vec![("a".into(), 4), ("b".into(), 2)]));
        assert_eq!(
            q.floors,
            Some(vec![Floor {
                topic: "a".into(),
                partition: 1,
                floor: 9
            }])
        );
        for bad in [
            r#"{"op":1}"#,
            r#"{"surprise":1}"#,
            r#"{"offset":1.0}"#,
            r#"{"offset":1e2}"#,
            r#"{"offset":"1"}"#,
            r#"{"value":"!"}"#,
            r#"{"items":[{"nope":1}]}"#,
            r#"[]"#,
        ] {
            assert!(
                decode(&gojson::parse(bad.as_bytes()).unwrap()).is_err(),
                "{bad}"
            );
        }
    }

    #[test]
    fn replies_are_the_go_driver_s_bytes() {
        assert_eq!(reply(None, None), b"{\"ok\":true}\n");
        let record = Value::Object(vec![
            ("topic".into(), Value::text("t")),
            ("partition".into(), Value::int(0)),
            ("offset".into(), Value::int(7)),
            ("key".into(), Value::text("")),
            ("value".into(), Value::bytes(Some(b"hi"))),
            ("id".into(), Value::text("")),
            ("time".into(), Value::text("1790000000000000000")),
        ]);
        assert_eq!(
            String::from_utf8(reply(Some(record), None)).unwrap(),
            "{\"ok\":true,\"result\":{\"topic\":\"t\",\"partition\":0,\"offset\":7,\"key\":\"\",\"value\":\"aGk=\",\"id\":\"\",\"time\":\"1790000000000000000\"}}\n"
        );
        let refused = Error::new(Kind::LaterFrames, "frames follow the <damage>");
        assert_eq!(
            String::from_utf8(reply(Some(Value::map(vec![("size".into(), Value::int(1))])), Some(&refused))).unwrap(),
            "{\"ok\":false,\"error\":\"later_frames\",\"message\":\"frames follow the \\u003cdamage\\u003e\",\"result\":{\"size\":1}}\n"
        );
    }

    #[test]
    fn serves_until_end_of_input_and_keeps_the_order_of_checks() {
        let (out, status) = served("{\"op\":\"close\"}\n{\"op\":\"nonsense\"}\n{\"op\":\"open\",\n \"dir\":\"d\",\"segmented\":true}\n\n");
        assert_eq!(status, Ok(()));
        let lines: Vec<&str> = out.lines().collect();
        assert_eq!(
            lines[0],
            r#"{"ok":false,"error":"other","message":"dost-conform: no node is open"}"#
        );
        assert_eq!(
            lines[1], lines[0],
            "an unknown request is refused for the missing node first, as in Go"
        );
        assert_eq!(
            lines[2],
            r#"{"ok":false,"error":"other","message":"dostoevsky: OpenSegmented is not ported yet (row 5)"}"#
        );
        assert_eq!(served("").1, Ok(()));
        assert!(served("{\"op\":\"close\"").1.is_err());
        assert!(served("{\"op\":\"close\",\"surprise\":1}\n").1.is_err());
        assert!(served("not json\n").1.is_err());
    }
}
