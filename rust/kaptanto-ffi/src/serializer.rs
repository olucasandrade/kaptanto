// Serde_json-backed event serialization helpers.
//
// decoder.rs handles full event serialization inline via serde_json::to_vec.
// This module provides a named helper that callers can use when they need to
// build ordered JSON objects from (name, value) pairs without duplicating the
// Map construction pattern.
//
// Key property: this crate does NOT enable serde_json's `preserve_order`
// feature (see Cargo.toml — only "derive" via serde, no "preserve_order" on
// serde_json), so `serde_json::Map` is backed by a `BTreeMap` and *sorts keys
// alphabetically* rather than preserving insertion order. That is a deliberate
// match for the pure-Go path: `decodeColumns` (internal/parser/pgoutput/types.go)
// returns a `map[string]any`, and Go's `encoding/json.Marshal` always emits
// map keys in sorted (alphabetical) order — Go map iteration order is random,
// but the stdlib json encoder normalizes it by sorting before serialization.
// So both sides independently converge on alphabetical key order; do not add
// `preserve_order` here, as that would make this path diverge from Go's actual
// output order.

use serde_json::{Map, Value};

/// Serialize a list of (name, value) fields to a JSON object byte vector.
/// serde_json::Map (no `preserve_order` feature) sorts keys alphabetically —
/// this matches Go's `encoding/json.Marshal` behavior for `map[string]any`,
/// which also always emits sorted keys. See the module doc comment above.
/// Returns None on serialization error (e.g., non-UTF-8 string values).
pub fn serialize_ordered_fields(fields: Vec<(String, Value)>) -> Option<Vec<u8>> {
    let map = Map::from_iter(fields);
    serde_json::to_vec(&Value::Object(map)).ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn test_serialize_ordered_fields_sorts_keys_alphabetically_like_go() {
        // Insertion order is deliberately NOT id, email, name — it is the
        // reverse-ish order pglogrepl would supply for these column names,
        // to prove the output order is independent of insertion order.
        let fields = vec![
            ("id".to_string(), json!("1")),
            ("email".to_string(), json!("test@example.com")),
            ("name".to_string(), json!("Alice")),
        ];
        let bytes = serialize_ordered_fields(fields).expect("serialize must succeed");
        let s = std::str::from_utf8(&bytes).expect("valid utf-8");
        // serde_json::Map (no `preserve_order` feature) sorts keys
        // alphabetically: email, id, name — matching Go's encoding/json
        // behavior for map[string]any (see module doc comment). This is the
        // actual, verified contract, not insertion/column-index order.
        let id_pos = s.find("\"id\"").expect("id key must be present");
        let email_pos = s.find("\"email\"").expect("email key must be present");
        let name_pos = s.find("\"name\"").expect("name key must be present");
        assert!(email_pos < id_pos, "keys must sort alphabetically: email before id");
        assert!(id_pos < name_pos, "keys must sort alphabetically: id before name");
    }

    #[test]
    fn test_serialize_ordered_fields_null_value() {
        let fields = vec![
            ("id".to_string(), json!("5")),
            ("description".to_string(), Value::Null),
        ];
        let bytes = serialize_ordered_fields(fields).expect("serialize must succeed");
        let s = std::str::from_utf8(&bytes).expect("valid utf-8");
        assert!(s.contains("\"description\":null"), "null field must serialize as null");
    }

    #[test]
    fn test_serialize_ordered_fields_empty() {
        let bytes = serialize_ordered_fields(vec![]).expect("empty fields must serialize");
        assert_eq!(bytes, b"{}");
    }
}
