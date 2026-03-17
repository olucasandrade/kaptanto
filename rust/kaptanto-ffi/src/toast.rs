use std::os::raw::c_uchar;

pub struct ToastCache {
    _private: (),
}

pub fn toast_new() -> *mut ToastCache {
    let cache = Box::new(ToastCache { _private: () });
    Box::into_raw(cache)
}

pub fn toast_set(
    _cache: *mut ToastCache,
    _rel_id: u32,
    _pk: *const c_uchar,
    _pk_len: usize,
    _row: *const c_uchar,
    _row_len: usize,
) {}

pub fn toast_get(
    _cache: *mut ToastCache,
    _rel_id: u32,
    _pk: *const c_uchar,
    _pk_len: usize,
    _out_len: *mut usize,
) -> *mut c_uchar {
    std::ptr::null_mut()
}

pub fn toast_free(cache: *mut ToastCache) {
    if !cache.is_null() {
        unsafe {
            let _ = Box::from_raw(cache);
        }
    }
}
