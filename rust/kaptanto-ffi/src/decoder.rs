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

    // Build ordered map preserving column-index order for deterministic JSON key order.
    // Use a Vec<(String, serde_json::Value)> then serialize as object.
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

    // Serialize as JSON object preserving insertion (column) order.
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
