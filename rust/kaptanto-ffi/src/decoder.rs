use std::os::raw::c_uchar;

const TYPE_NULL: u8 = b'n';
const TYPE_TOAST: u8 = b'u';
const TYPE_TEXT: u8 = b't';
const TYPE_BINARY: u8 = b'b';

pub fn decode_serialize(
    col_data: *const c_uchar,
    col_len: usize,
    schema_json: *const c_uchar,
    schema_len: usize,
    out_len: *mut usize,
) -> *mut c_uchar {
    if col_data.is_null() || schema_json.is_null() || out_len.is_null() {
        return std::ptr::null_mut();
    }

    let col_bytes = unsafe { std::slice::from_raw_parts(col_data, col_len) };
    let schema_bytes = unsafe { std::slice::from_raw_parts(schema_json, schema_len) };

    // Parse schema: ["col1", "col2", ...]
    let names: Vec<String> = match serde_json::from_slice(schema_bytes) {
        Ok(v) => v,
        Err(_) => return std::ptr::null_mut(),
    };

    // Parse column count
    if col_bytes.len() < 4 {
        return std::ptr::null_mut();
    }
    let num_cols = u32::from_be_bytes([col_bytes[0], col_bytes[1], col_bytes[2], col_bytes[3]]) as usize;
    let mut pos = 4usize;

    // Collect fields in column-index order, then hand off to serde_json::Map.
    // NOTE: Map::from_iter below does NOT preserve this insertion order — the
    // crate does not enable serde_json's `preserve_order` feature, so the
    // resulting Map is BTreeMap-backed and sorts keys alphabetically on
    // serialization. That sort is intentional: it matches Go's
    // encoding/json.Marshal(map[string]any) behavior, which also always
    // emits sorted keys (see serializer.rs's module doc comment for the
    // full explanation and the regression test that pins this).
    let mut fields: Vec<(String, serde_json::Value)> = Vec::with_capacity(num_cols);

    for i in 0..num_cols {
        if pos + 5 > col_bytes.len() {
            return std::ptr::null_mut();
        }
        let data_type = col_bytes[pos];
        pos += 1;
        let data_len = u32::from_be_bytes([
            col_bytes[pos], col_bytes[pos + 1], col_bytes[pos + 2], col_bytes[pos + 3],
        ]) as usize;
        pos += 4;

        if pos + data_len > col_bytes.len() {
            return std::ptr::null_mut();
        }
        let data = &col_bytes[pos..pos + data_len];
        pos += data_len;

        let name = names.get(i).cloned().unwrap_or_else(|| format!("col{}", i));

        let value = match data_type {
            TYPE_NULL | TYPE_TOAST => serde_json::Value::Null,
            TYPE_TEXT => {
                let s = match std::str::from_utf8(data) {
                    Ok(s) => s,
                    Err(_) => return std::ptr::null_mut(),
                };
                serde_json::Value::String(s.to_string())
            }
            TYPE_BINARY => {
                // Encode binary as base64 to match Go's json.Marshal([]byte{...}) behavior.
                let encoded = base64_encode(data);
                serde_json::Value::String(encoded)
            }
            _ => serde_json::Value::Null,
        };

        fields.push((name, value));
    }

    // Serialize as a JSON object; keys are sorted alphabetically to match Go's
    // encoding/json.Marshal(map[string]any) — see the note above.
    let obj = serde_json::Map::from_iter(fields);
    let json_bytes = match serde_json::to_vec(&serde_json::Value::Object(obj)) {
        Ok(b) => b,
        Err(_) => return std::ptr::null_mut(),
    };

    let len = json_bytes.len();
    let mut v = json_bytes;
    let ptr = v.as_mut_ptr();
    std::mem::forget(v);
    unsafe { *out_len = len; }
    ptr
}

fn base64_encode(data: &[u8]) -> String {
    // Minimal base64 without external crate — Go's encoding/json encodes []byte as base64.
    const CHARS: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = Vec::with_capacity((data.len() + 2) / 3 * 4);
    for chunk in data.chunks(3) {
        let b0 = chunk[0] as usize;
        let b1 = if chunk.len() > 1 { chunk[1] as usize } else { 0 };
        let b2 = if chunk.len() > 2 { chunk[2] as usize } else { 0 };
        out.push(CHARS[(b0 >> 2) & 0x3F]);
        out.push(CHARS[((b0 << 4) | (b1 >> 4)) & 0x3F]);
        if chunk.len() > 1 {
            out.push(CHARS[((b1 << 2) | (b2 >> 6)) & 0x3F]);
        } else {
            out.push(b'=');
        }
        if chunk.len() > 2 {
            out.push(CHARS[b2 & 0x3F]);
        } else {
            out.push(b'=');
        }
    }
    String::from_utf8(out).unwrap()
}

#[cfg(test)]
mod tests {
    use super::*;

    // --- fixture builders, mirroring internal/parser/pgoutput/parser_test.go's
    // encodeColumns/encodeSchema wire-format helpers (Go side: ffi_rust.go) ---

    fn encode_columns(cols: &[(u8, &[u8])]) -> Vec<u8> {
        let mut buf = Vec::new();
        buf.extend_from_slice(&(cols.len() as u32).to_be_bytes());
        for (data_type, data) in cols {
            buf.push(*data_type);
            buf.extend_from_slice(&(data.len() as u32).to_be_bytes());
            buf.extend_from_slice(data);
        }
        buf
    }

    fn encode_schema(names: &[&str]) -> Vec<u8> {
        serde_json::to_vec(names).expect("schema must serialize")
    }

    // Runs decode_serialize over the given columns/schema and returns the
    // decoded JSON as a serde_json::Value for easy field-by-field assertions
    // — the same "parse then compare fields" strategy parser_ffi_test.go uses
    // on the Go side, rather than asserting on raw JSON bytes.
    fn decode(cols: &[(u8, &[u8])], names: &[&str]) -> Option<serde_json::Value> {
        let col_bytes = encode_columns(cols);
        let schema_bytes = encode_schema(names);
        let mut out_len: usize = 0;
        let ptr = decode_serialize(
            col_bytes.as_ptr(),
            col_bytes.len(),
            schema_bytes.as_ptr(),
            schema_bytes.len(),
            &mut out_len,
        );
        if ptr.is_null() {
            return None;
        }
        let bytes = unsafe { std::slice::from_raw_parts(ptr, out_len).to_vec() };
        unsafe {
            let _ = Vec::from_raw_parts(ptr, out_len, out_len);
        }
        Some(serde_json::from_slice(&bytes).expect("decoder output must be valid JSON"))
    }

    #[test]
    fn test_decode_serialize_text_columns() {
        // Mirrors TestInsertProducesChangeEvent's after-row assertions
        // (parser_test.go): every text column's value must round-trip intact,
        // regardless of key order in the output (see serializer.rs's doc
        // comment on why key order is alphabetical, not column-index order).
        let v = decode(
            &[(TYPE_TEXT, b"1"), (TYPE_TEXT, b"Widget"), (TYPE_TEXT, b"desc")],
            &["id", "name", "content"],
        )
        .expect("decode must succeed");
        assert_eq!(v["id"], "1");
        assert_eq!(v["name"], "Widget");
        assert_eq!(v["content"], "desc");
    }

    #[test]
    fn test_decode_serialize_null_column() {
        // Mirrors TestNullColumnInInsert: a null column ('n') must decode to
        // JSON null, not be omitted from the object.
        let v = decode(&[(TYPE_TEXT, b"7"), (TYPE_NULL, b"")], &["id", "description"])
            .expect("decode must succeed");
        assert_eq!(v["id"], "7");
        assert!(v["description"].is_null(), "null column must decode to JSON null");
    }

    #[test]
    fn test_decode_serialize_toast_column_currently_decodes_as_null() {
        // KNOWN GAP (tracked as residual risk by the rust-ffi-testing fix
        // plan, not fixed here — out of scope for a testing-infrastructure
        // change): unlike the Go path (decodeColumns merges an unchanged
        // TOAST ('u') column from TOASTCache via the prevRow parameter — see
        // internal/parser/pgoutput/types.go and parser.go's handleUpdate),
        // decode_serialize has no cache handle or previous-row parameter at
        // all. A TOAST column always decodes as null here, even when a
        // fresher cached value exists. ffi_rust.go's decodeAndSerializeRow
        // never calls toast_set/toast_get either (see toast.rs's test module
        // note). This test pins the CURRENT behavior so any accidental
        // further regression is caught — it is not a statement that TOAST
        // merging works on the Rust path. It does not.
        let v = decode(&[(TYPE_TEXT, b"10"), (TYPE_TOAST, b"")], &["id", "content"])
            .expect("decode must succeed");
        assert_eq!(v["id"], "10");
        assert!(
            v["content"].is_null(),
            "TOAST column decodes as null on the Rust path today — no merge occurs"
        );
    }

    #[test]
    fn test_decode_serialize_binary_column_base64_encoded() {
        let v = decode(&[(TYPE_BINARY, b"\x00\x01\x02\xff")], &["payload"]).expect("decode must succeed");
        // Go's encoding/json marshals []byte as standard base64.
        assert_eq!(v["payload"], "AAEC/w==");
    }

    #[test]
    fn test_decode_serialize_missing_schema_name_falls_back_to_col_index() {
        // Fewer names than columns: decode_serialize falls back to "col{i}"
        // for any column past the end of the schema array (line 56).
        let v = decode(&[(TYPE_TEXT, b"a"), (TYPE_TEXT, b"b")], &["only_one"]).expect("decode must succeed");
        assert_eq!(v["only_one"], "a");
        assert_eq!(v["col1"], "b");
    }

    #[test]
    fn test_decode_serialize_empty_columns_returns_empty_object() {
        let v = decode(&[], &[]).expect("decode must succeed");
        assert_eq!(v, serde_json::json!({}));
    }

    #[test]
    fn test_decode_serialize_null_pointer_inputs_return_null() {
        let mut out_len: usize = 0;
        let ptr = decode_serialize(std::ptr::null(), 0, std::ptr::null(), 0, &mut out_len);
        assert!(ptr.is_null(), "null col_data/schema_json must return null, not panic");
    }

    #[test]
    fn test_decode_serialize_malformed_schema_json_returns_null() {
        let col_bytes = encode_columns(&[(TYPE_TEXT, b"x")]);
        let bad_schema = b"not valid json";
        let mut out_len: usize = 0;
        let ptr = decode_serialize(
            col_bytes.as_ptr(),
            col_bytes.len(),
            bad_schema.as_ptr(),
            bad_schema.len(),
            &mut out_len,
        );
        assert!(ptr.is_null(), "malformed schema JSON must return null, not panic");
    }

    #[test]
    fn test_decode_serialize_truncated_column_data_returns_null() {
        // Declares 1 column but supplies no type/length/data bytes at all.
        let mut col_bytes = Vec::new();
        col_bytes.extend_from_slice(&1u32.to_be_bytes());
        let schema_bytes = encode_schema(&["id"]);
        let mut out_len: usize = 0;
        let ptr = decode_serialize(
            col_bytes.as_ptr(),
            col_bytes.len(),
            schema_bytes.as_ptr(),
            schema_bytes.len(),
            &mut out_len,
        );
        assert!(ptr.is_null(), "truncated column payload must return null, not panic or read OOB");
    }

    #[test]
    fn test_decode_serialize_hostile_identifier_column_name() {
        // Mirrors the Go parser suite's hostile-identifier concern: a column
        // name containing characters that could break naive string
        // concatenation (quotes, backslashes) must still round-trip safely
        // through serde_json's proper escaping.
        let v = decode(&[(TYPE_TEXT, b"v")], &["weird\"name\\with/slash"]).expect("decode must succeed");
        assert_eq!(v["weird\"name\\with/slash"], "v");
    }

    #[test]
    fn test_base64_encode_matches_standard_padding() {
        assert_eq!(base64_encode(b""), "");
        assert_eq!(base64_encode(b"f"), "Zg==");
        assert_eq!(base64_encode(b"fo"), "Zm8=");
        assert_eq!(base64_encode(b"foo"), "Zm9v");
        assert_eq!(base64_encode(b"foob"), "Zm9vYg==");
        assert_eq!(base64_encode(b"fooba"), "Zm9vYmE=");
        assert_eq!(base64_encode(b"foobar"), "Zm9vYmFy");
    }
}
