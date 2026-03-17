// Serde_json-backed event serialization helpers.
//
// decoder.rs handles full event serialization inline via serde_json::to_vec.
// This module provides a named helper that callers can use when they need to
// build ordered JSON objects from (name, value) pairs without duplicating the
// Map construction pattern.
//
// Key property: serde_json::Map preserves insertion order, so serializing a
// Vec<(String, Value)> produces deterministic JSON key ordering that matches
// the column-index order supplied by pglogrepl. This is the correct criterion
// for structural equivalence with the pure-Go path (which uses column-index
// iteration order when building the row map in decodeColumns).

use serde_json::{Map, Value};

/// Serialize an ordered list of (name, value) fields to a JSON object byte vector.
/// Column-index order is preserved — serde_json::Map preserves insertion order.
/// This is deterministic given a deterministic column-index order from pglogrepl.
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
    fn test_serialize_ordered_fields_preserves_insertion_order() {
        let fields = vec![
            ("id".to_string(), json!("1")),
            ("email".to_string(), json!("test@example.com")),
            ("name".to_string(), json!("Alice")),
        ];
        let bytes = serialize_ordered_fields(fields).expect("serialize must succeed");
        let s = std::str::from_utf8(&bytes).expect("valid utf-8");
        // Verify key order: id before email before name.
        let id_pos = s.find("\"id\"").expect("id key must be present");
        let email_pos = s.find("\"email\"").expect("email key must be present");
        let name_pos = s.find("\"name\"").expect("name key must be present");
        assert!(id_pos < email_pos, "id must come before email");
        assert!(email_pos < name_pos, "email must come before name");
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
