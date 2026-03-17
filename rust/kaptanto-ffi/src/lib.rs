use std::os::raw::c_uchar;

pub mod decoder;
pub mod toast;
pub mod serializer;

// Re-export the opaque TOAST cache handle.
pub use toast::ToastCache;

/// Free a buffer allocated by kaptanto_decode_serialize or kaptanto_toast_get.
/// Caller MUST call this on every non-null pointer returned by those functions.
#[no_mangle]
pub extern "C" fn kaptanto_free_buf(ptr: *mut c_uchar, len: usize) {
    if ptr.is_null() {
        return;
    }
    unsafe {
        let _ = Vec::from_raw_parts(ptr, len, len);
    }
}

/// Decode pgoutput column data and serialize the row to JSON.
/// col_data: length-prefixed binary encoding of columns (see decoder.rs format).
/// schema_json: JSON array of column names in column-index order.
/// Returns a heap-allocated JSON byte slice; caller must free with kaptanto_free_buf.
/// Returns null on error.
#[no_mangle]
pub extern "C" fn kaptanto_decode_serialize(
    col_data: *const c_uchar,
    col_len: usize,
    schema_json: *const c_uchar,
    schema_len: usize,
    out_len: *mut usize,
) -> *mut c_uchar {
    std::panic::catch_unwind(|| {
        decoder::decode_serialize(col_data, col_len, schema_json, schema_len, out_len)
    })
    .unwrap_or(std::ptr::null_mut())
}

/// Allocate a new TOAST cache. Returns an opaque handle.
/// Caller owns the handle and must free it with kaptanto_toast_free.
#[no_mangle]
pub extern "C" fn kaptanto_toast_new() -> *mut ToastCache {
    toast::toast_new()
}

/// Store row bytes in the TOAST cache under (rel_id, pk).
#[no_mangle]
pub extern "C" fn kaptanto_toast_set(
    cache: *mut ToastCache,
    rel_id: u32,
    pk: *const c_uchar,
    pk_len: usize,
    row: *const c_uchar,
    row_len: usize,
) {
    let _ = std::panic::catch_unwind(|| {
        toast::toast_set(cache, rel_id, pk, pk_len, row, row_len)
    });
}

/// Retrieve cached row bytes from the TOAST cache.
/// Returns null if not found. Caller must free with kaptanto_free_buf.
#[no_mangle]
pub extern "C" fn kaptanto_toast_get(
    cache: *mut ToastCache,
    rel_id: u32,
    pk: *const c_uchar,
    pk_len: usize,
    out_len: *mut usize,
) -> *mut c_uchar {
    std::panic::catch_unwind(|| {
        toast::toast_get(cache, rel_id, pk, pk_len, out_len)
    })
    .unwrap_or(std::ptr::null_mut())
}

/// Free a TOAST cache handle allocated by kaptanto_toast_new.
#[no_mangle]
pub extern "C" fn kaptanto_toast_free(cache: *mut ToastCache) {
    toast::toast_free(cache)
}
