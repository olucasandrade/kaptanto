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

#[cfg(test)]
mod tests {
    use super::*;

    // Helper: allocate a fresh cache for a test and free it at the end via a
    // small RAII guard, so a panicking assertion doesn't leak the handle.
    struct CacheGuard(*mut ToastCache);
    impl Drop for CacheGuard {
        fn drop(&mut self) {
            toast_free(self.0);
        }
    }

    fn set(cache: &CacheGuard, rel_id: u32, pk: &[u8], row: &[u8]) {
        toast_set(cache.0, rel_id, pk.as_ptr(), pk.len(), row.as_ptr(), row.len());
    }

    fn get(cache: &CacheGuard, rel_id: u32, pk: &[u8]) -> Option<Vec<u8>> {
        let mut out_len: usize = 0;
        let ptr = toast_get(cache.0, rel_id, pk.as_ptr(), pk.len(), &mut out_len);
        if ptr.is_null() {
            return None;
        }
        let bytes = unsafe { std::slice::from_raw_parts(ptr, out_len).to_vec() };
        // toast_get hands back a Go/Rust-owned copy per lib.rs's contract (the
        // caller frees via kaptanto_free_buf); reclaim it here the same way
        // kaptanto_free_buf does, to avoid leaking in the test.
        unsafe {
            let _ = Vec::from_raw_parts(ptr, out_len, out_len);
        }
        Some(bytes)
    }

    #[test]
    fn test_toast_set_get_roundtrip() {
        let cache = CacheGuard(toast_new());
        set(&cache, 1, b"pk-1", b"cached row bytes");
        let got = get(&cache, 1, b"pk-1").expect("value must be present after set");
        assert_eq!(got, b"cached row bytes");
    }

    #[test]
    fn test_toast_get_missing_key_returns_none() {
        let cache = CacheGuard(toast_new());
        assert!(get(&cache, 1, b"missing").is_none(), "unset key must return None");
    }

    #[test]
    fn test_toast_namespaced_by_relation_id() {
        // Same primary-key bytes under two different relation IDs must not collide
        // — mirrors BKF-02/CHK-01-adjacent partitioning discipline used elsewhere
        // in the Go event log (FNV-1a partition keys include relation identity).
        let cache = CacheGuard(toast_new());
        set(&cache, 1, b"pk", b"row-for-rel-1");
        set(&cache, 2, b"pk", b"row-for-rel-2");
        assert_eq!(get(&cache, 1, b"pk").unwrap(), b"row-for-rel-1");
        assert_eq!(get(&cache, 2, b"pk").unwrap(), b"row-for-rel-2");
    }

    #[test]
    fn test_toast_set_overwrites_existing_key() {
        // Mirrors the Go-side TOASTCache.Set semantics used on every UPDATE:
        // the freshest decoded row always replaces the previous cache entry.
        let cache = CacheGuard(toast_new());
        set(&cache, 1, b"pk-1", b"first version");
        set(&cache, 1, b"pk-1", b"second version");
        assert_eq!(get(&cache, 1, b"pk-1").unwrap(), b"second version");
    }

    #[test]
    fn test_toast_different_pk_bytes_do_not_collide() {
        let cache = CacheGuard(toast_new());
        set(&cache, 1, b"pk-a", b"row-a");
        set(&cache, 1, b"pk-b", b"row-b");
        assert_eq!(get(&cache, 1, b"pk-a").unwrap(), b"row-a");
        assert_eq!(get(&cache, 1, b"pk-b").unwrap(), b"row-b");
    }

    #[test]
    fn test_toast_null_cache_handle_is_safe() {
        // toast_set/toast_get must not dereference a null cache pointer — the
        // Go glue passes a null handle if allocation ever failed upstream.
        let mut out_len: usize = 0;
        let pk = b"pk";
        let row = b"row";
        toast_set(std::ptr::null_mut(), 1, pk.as_ptr(), pk.len(), row.as_ptr(), row.len());
        let ptr = toast_get(std::ptr::null_mut(), 1, pk.as_ptr(), pk.len(), &mut out_len);
        assert!(ptr.is_null(), "get against a null cache handle must return null, not panic");
        toast_free(std::ptr::null_mut()); // must not panic
    }

    // NOTE (residual risk, tracked by the rust-ffi-testing fix plan): this
    // crate has no toast_delete/evict primitive at all, even though the Go
    // twin (internal/parser/pgoutput/types.go's TOASTCache.Delete, called
    // from Parser.handleDelete) evicts the cache entry on every DELETE. More
    // fundamentally, nothing in decoder.rs or ffi_rust.go's
    // decodeAndSerializeRow ever calls toast_set/toast_get at all — this
    // cache is fully wired and tested here in isolation, but orphaned from
    // the actual decode path. See decoder.rs's test module for the pinned
    // (currently-null) TOAST-column decode behavior this implies.
}
