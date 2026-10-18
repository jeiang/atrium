package sqlitevec

/*
#cgo CFLAGS: -DSQLITE_CORE -DSQLITE_VEC_STATIC
#cgo linux LDFLAGS: -lm
#cgo linux,amd64 CFLAGS: -mavx -DSQLITE_VEC_ENABLE_AVX
#cgo arm64 CFLAGS: -DSQLITE_VEC_ENABLE_NEON

// sqlite-vec.c typedefs u_int8_t and friends, which musl does not declare;
// the __COSMOPOLITAN__ guard is the only switch that skips them.
#cgo linux CFLAGS: -D__COSMOPOLITAN__

#include "sqlite-vec.h"
*/
import "C"

import "unsafe"

const Version = C.SQLITE_VEC_VERSION

// Auto registers vec0 and the vec_* functions on every connection opened
// afterwards; the sqlite3 symbols come from github.com/mattn/go-sqlite3.
func Auto() {
	C.sqlite3_auto_extension((*[0]byte)(unsafe.Pointer(C.sqlite3_vec_init)))
}
