use fnv::FnvHashMap;
use std::os::raw::c_uchar;

/// Opaque TOAST cache. Keyed by (relation_id, pk_bytes).
pub struct ToastCache {
    inner: FnvHashMap<(u32, Vec<u8>), Vec<u8>>,
}

pub fn toast_new() -> *mut ToastCache {
    let cache = Box::new(ToastCache {
        inner: FnvHashMap::default(),
    });
    Box::into_raw(cache)
}

pub fn toast_set(
    cache: *mut ToastCache,
    rel_id: u32,
    pk: *const c_uchar,
    pk_len: usize,
    row: *const c_uchar,
    row_len: usize,
) {
    if cache.is_null() || pk.is_null() || row.is_null() {
        return;
    }
    let cache = unsafe { &mut *cache };
    let pk_bytes = unsafe { std::slice::from_raw_parts(pk, pk_len).to_vec() };
    let row_bytes = unsafe { std::slice::from_raw_parts(row, row_len).to_vec() };
    cache.inner.insert((rel_id, pk_bytes), row_bytes);
}

pub fn toast_get(
    cache: *mut ToastCache,
    rel_id: u32,
    pk: *const c_uchar,
    pk_len: usize,
    out_len: *mut usize,
) -> *mut c_uchar {
    if cache.is_null() || pk.is_null() || out_len.is_null() {
        return std::ptr::null_mut();
    }
    let cache = unsafe { &*cache };
    let pk_bytes = unsafe { std::slice::from_raw_parts(pk, pk_len) };
    match cache.inner.get(&(rel_id, pk_bytes.to_vec())) {
        None => std::ptr::null_mut(),
        Some(row_bytes) => {
            let len = row_bytes.len();
            let mut v = row_bytes.clone();
            let ptr = v.as_mut_ptr();
            std::mem::forget(v);
            unsafe { *out_len = len; }
            ptr
        }
    }
}

pub fn toast_free(cache: *mut ToastCache) {
    if !cache.is_null() {
        unsafe {
            let _ = Box::from_raw(cache);
        }
    }
}
