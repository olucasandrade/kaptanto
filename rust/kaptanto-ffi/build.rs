fn main() {
    let crate_dir = std::env::var("CARGO_MANIFEST_DIR").unwrap();
    let out_dir = std::path::PathBuf::from(&crate_dir);
    std::fs::create_dir_all(out_dir.join("include")).expect("create include dir");
    cbindgen::Builder::new()
        .with_crate(&crate_dir)
        .with_language(cbindgen::Language::C)
        .generate()
        .expect("cbindgen failed")
        .write_to_file(out_dir.join("include/kaptanto_ffi.h"));
}
