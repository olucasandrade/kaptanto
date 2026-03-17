use std::os::raw::c_uchar;

pub fn decode_serialize(
    _col_data: *const c_uchar,
    _col_len: usize,
    _schema_json: *const c_uchar,
    _schema_len: usize,
    _out_len: *mut usize,
) -> *mut c_uchar {
    std::ptr::null_mut()
}
