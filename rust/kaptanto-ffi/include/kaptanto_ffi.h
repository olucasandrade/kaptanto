#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

/**
 * Opaque TOAST cache. Keyed by (relation_id, pk_bytes).
 */
typedef struct ToastCache ToastCache;

/**
 * Free a buffer allocated by kaptanto_decode_serialize or kaptanto_toast_get.
 * Caller MUST call this on every non-null pointer returned by those functions.
 */
void kaptanto_free_buf(unsigned char *ptr, uintptr_t len);

/**
 * Decode pgoutput column data and serialize the row to JSON.
 * col_data: length-prefixed binary encoding of columns (see decoder.rs format).
 * schema_json: JSON array of column names in column-index order.
 * Returns a heap-allocated JSON byte slice; caller must free with kaptanto_free_buf.
 * Returns null on error.
 */
unsigned char *kaptanto_decode_serialize(const unsigned char *col_data,
                                         uintptr_t col_len,
                                         const unsigned char *schema_json,
                                         uintptr_t schema_len,
                                         uintptr_t *out_len);

/**
 * Allocate a new TOAST cache. Returns an opaque handle.
 * Caller owns the handle and must free it with kaptanto_toast_free.
 */
struct ToastCache *kaptanto_toast_new(void);

/**
 * Store row bytes in the TOAST cache under (rel_id, pk).
 */
void kaptanto_toast_set(struct ToastCache *cache,
                        uint32_t rel_id,
                        const unsigned char *pk,
                        uintptr_t pk_len,
                        const unsigned char *row,
                        uintptr_t row_len);

/**
 * Retrieve cached row bytes from the TOAST cache.
 * Returns null if not found. Caller must free with kaptanto_free_buf.
 */
unsigned char *kaptanto_toast_get(struct ToastCache *cache,
                                  uint32_t rel_id,
                                  const unsigned char *pk,
                                  uintptr_t pk_len,
                                  uintptr_t *out_len);

/**
 * Free a TOAST cache handle allocated by kaptanto_toast_new.
 */
void kaptanto_toast_free(struct ToastCache *cache);
