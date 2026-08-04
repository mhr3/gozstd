package gozstd

/*
#cgo CFLAGS: -O3

#cgo noescape ZSTD_compressCCtx
#cgo nocallback ZSTD_compressCCtx
#cgo noescape ZSTD_compress_usingCDict
#cgo nocallback ZSTD_compress_usingCDict
#cgo noescape ZSTD_decompressDCtx
#cgo nocallback ZSTD_decompressDCtx
#cgo noescape ZSTD_decompress_usingDDict
#cgo nocallback ZSTD_decompress_usingDDict
#cgo noescape ZSTD_findDecompressedSize
#cgo nocallback ZSTD_findDecompressedSize

#define ZSTD_STATIC_LINKING_ONLY
#include "zstd.h"
#include "zstd_errors.h"

*/
import "C"

import (
	"fmt"
	"io"
	"runtime"
	"sync"
	"unsafe"
)

// DefaultCompressionLevel is the default compression level.
const DefaultCompressionLevel = 3 // Obtained from ZSTD_CLEVEL_DEFAULT.

const maxFrameContentSize = 256 << 20 // 256 MB

// Compress appends compressed src to dst and returns the result.
func Compress(dst, src []byte) []byte {
	return compressDictLevel(dst, src, nil, DefaultCompressionLevel)
}

// CompressLevel appends compressed src to dst and returns the result.
//
// The given compressionLevel is used for the compression.
func CompressLevel(dst, src []byte, compressionLevel int) []byte {
	return compressDictLevel(dst, src, nil, compressionLevel)
}

// CompressDict appends compressed src to dst and returns the result.
//
// The given dictionary is used for the compression.
func CompressDict(dst, src []byte, cd *CDict) []byte {
	return compressDictLevel(dst, src, cd, 0)
}

func compressDictLevel(dst, src []byte, cd *CDict, compressionLevel int) []byte {
	var cctx, cctxDict *cctxWrapper
	if cd == nil {
		cctx = cctxPool.Get().(*cctxWrapper)
	} else {
		cctxDict = cctxDictPool.Get().(*cctxWrapper)
	}

	dst = compress(cctx, cctxDict, dst, src, cd, compressionLevel)

	if cd == nil {
		cctxPool.Put(cctx)
	} else {
		cctxDictPool.Put(cctxDict)
	}
	return dst
}

var cctxPool = &sync.Pool{
	New: newCCtx,
}

var cctxDictPool = &sync.Pool{
	New: newCCtx,
}

func newCCtx() interface{} {
	cctx := C.ZSTD_createCCtx()
	cw := &cctxWrapper{
		cctx: cctx,
	}
	runtime.SetFinalizer(cw, freeCCtx)
	return cw
}

func freeCCtx(cw *cctxWrapper) {
	C.ZSTD_freeCCtx(cw.cctx)
	cw.cctx = nil
}

type cctxWrapper struct {
	cctx *C.ZSTD_CCtx
}

func compress(cctx, cctxDict *cctxWrapper, dst, src []byte, cd *CDict, compressionLevel int) []byte {
	if len(src) == 0 {
		return dst
	}

	dstLen := len(dst)
	if cap(dst) > dstLen {
		// Fast path - try compressing without dst resize.
		result := compressInternal(cctx, cctxDict, dst[dstLen:cap(dst)], src, cd, compressionLevel, false)
		compressedSize := int(result)
		if compressedSize >= 0 {
			// All OK.
			return dst[:dstLen+compressedSize]
		}
		if C.ZSTD_getErrorCode(result) != C.ZSTD_error_dstSize_tooSmall {
			// Unexpected error.
			panic(fmt.Errorf("BUG: unexpected error during compression with cd=%p: %s", cd, errStr(result)))
		}
	}

	// Slow path - resize dst to fit compressed data.
	compressBound := int(C.ZSTD_compressBound(C.size_t(len(src)))) + 1
	if n := dstLen + compressBound - cap(dst) + dstLen; n > 0 {
		// This should be optimized since go 1.11 - see https://golang.org/doc/go1.11#performance-compiler.
		dst = append(dst[:cap(dst)], make([]byte, n)...)
	}

	result := compressInternal(cctx, cctxDict, dst[dstLen:dstLen+compressBound], src, cd, compressionLevel, true)
	compressedSize := int(result)
	dst = dst[:dstLen+compressedSize]
	if cap(dst)-len(dst) > 4096 {
		// Re-allocate dst in order to remove superflouos capacity and reduce memory usage.
		dst = append([]byte{}, dst...)
	}
	return dst
}

func compressInternal(cctx, cctxDict *cctxWrapper, dst, src []byte, cd *CDict, compressionLevel int, mustSucceed bool) C.size_t {
	dstPtr := unsafe.Pointer(unsafe.SliceData(dst))
	srcPtr := unsafe.Pointer(unsafe.SliceData(src))

	if cd != nil {
		result := C.ZSTD_compress_usingCDict(
			cctxDict.cctx,
			dstPtr,
			C.size_t(cap(dst)),
			srcPtr,
			C.size_t(len(src)),
			cd.p)
		if mustSucceed {
			ensureNoError("ZSTD_compress_usingCDict", result)
		}
		return result
	}

	result := C.ZSTD_compressCCtx(
		cctx.cctx,
		dstPtr,
		C.size_t(cap(dst)),
		srcPtr,
		C.size_t(len(src)),
		C.int(compressionLevel))
	if mustSucceed {
		ensureNoError("ZSTD_compressCCtx", result)
	}

	return result
}

// Decompress appends decompressed src to dst and returns the result.
func Decompress(dst, src []byte) ([]byte, error) {
	return DecompressDict(dst, src, nil)
}

// DecompressDict appends decompressed src to dst and returns the result.
//
// The given dictionary dd is used for the decompression.
func DecompressDict(dst, src []byte, dd *DDict) ([]byte, error) {
	var dctx, dctxDict *dctxWrapper
	if dd == nil {
		dctx = dctxPool.Get().(*dctxWrapper)
	} else {
		dctxDict = dctxDictPool.Get().(*dctxWrapper)
	}

	var err error
	dst, err = decompress(dctx, dctxDict, dst, src, dd)

	if dd == nil {
		dctxPool.Put(dctx)
	} else {
		dctxDictPool.Put(dctxDict)
	}
	return dst, err
}

var dctxPool = &sync.Pool{
	New: newDCtx,
}

var dctxDictPool = &sync.Pool{
	New: newDCtx,
}

func newDCtx() interface{} {
	dctx := C.ZSTD_createDCtx()
	dw := &dctxWrapper{
		dctx: dctx,
	}
	runtime.SetFinalizer(dw, freeDCtx)
	return dw
}

func freeDCtx(dw *dctxWrapper) {
	C.ZSTD_freeDCtx(dw.dctx)
	dw.dctx = nil
}

type dctxWrapper struct {
	dctx *C.ZSTD_DCtx
}

func decompress(dctx, dctxDict *dctxWrapper, dst, src []byte, dd *DDict) ([]byte, error) {
	if len(src) == 0 {
		return dst, nil
	}

	dstLen := len(dst)
	if cap(dst) > dstLen {
		// Fast path - try decompressing without dst resize.
		result := decompressInternal(dctx, dctxDict, dst[dstLen:cap(dst)], src, dd)
		decompressedSize := int(result)
		if decompressedSize >= 0 {
			// All OK.
			return dst[:dstLen+decompressedSize], nil
		}

		if C.ZSTD_getErrorCode(result) != C.ZSTD_error_dstSize_tooSmall {
			// Error during decompression.
			return dst[:dstLen], fmt.Errorf("decompression error: %s", errStr(result))
		}
	}

	// Slow path - resize dst to fit decompressed data.
	srcPtr := unsafe.Pointer(unsafe.SliceData(src))
	contentSize := C.ZSTD_findDecompressedSize(srcPtr, C.size_t(len(src)))
	switch {
	case contentSize == C.ZSTD_CONTENTSIZE_UNKNOWN || contentSize > maxFrameContentSize:
		return streamDecompress(dst, src, dd)
	case contentSize == C.ZSTD_CONTENTSIZE_ERROR:
		return dst, fmt.Errorf("cannot decompress invalid src")
	}
	decompressBound := int(contentSize) + 1

	if n := dstLen + decompressBound - cap(dst); n > 0 {
		// This should be optimized since go 1.11 - see https://golang.org/doc/go1.11#performance-compiler.
		dst = append(dst[:cap(dst)], make([]byte, n)...)
	}

	result := decompressInternal(dctx, dctxDict, dst[dstLen:dstLen+decompressBound], src, dd)
	decompressedSize := int(result)
	if decompressedSize >= 0 {
		dst = dst[:dstLen+decompressedSize]
		if cap(dst)-len(dst) > 4096 {
			// Re-allocate dst in order to remove superflouos capacity and reduce memory usage.
			dst = append([]byte{}, dst...)
		}
		return dst, nil
	}

	// Error during decompression.
	return dst[:dstLen], fmt.Errorf("decompression error: %s", errStr(result))
}

func decompressInternal(dctx, dctxDict *dctxWrapper, dst, src []byte, dd *DDict) C.size_t {
	var (
		dstPtr = unsafe.Pointer(unsafe.SliceData(dst))
		srcPtr = unsafe.Pointer(unsafe.SliceData(src))
		n      C.size_t
	)
	if dd != nil {
		n = C.ZSTD_decompress_usingDDict(
			dctxDict.dctx,
			dstPtr,
			C.size_t(cap(dst)),
			srcPtr,
			C.size_t(len(src)),
			dd.p)
	} else {
		n = C.ZSTD_decompressDCtx(
			dctx.dctx,
			dstPtr,
			C.size_t(cap(dst)),
			srcPtr,
			C.size_t(len(src)))
	}
	return n
}

func errStr(result C.size_t) string {
	errCode := C.ZSTD_getErrorCode(result)
	errCStr := C.ZSTD_getErrorString(errCode)
	return C.GoString(errCStr)
}

func ensureNoError(funcName string, result C.size_t) {
	if zstdIsError(result) {
		panic(fmt.Errorf("BUG: unexpected error in %s: %s", funcName, errStr(result)))
	}
}

func zstdIsError(result C.size_t) bool {
	if int(result) >= 0 {
		// Fast path - avoid calling C function.
		return false
	}
	return C.ZSTD_isError(result) != 0
}

func streamDecompress(dst, src []byte, dd *DDict) ([]byte, error) {
	sd := getStreamDecompressor(dd)
	sd.dst = dst
	sd.src = src
	_, err := sd.zr.WriteTo(sd)
	dst = sd.dst
	putStreamDecompressor(sd)
	return dst, err
}

type streamDecompressor struct {
	dst       []byte
	src       []byte
	srcOffset int

	zr *Reader
}

type srcReader streamDecompressor

func (sr *srcReader) Read(p []byte) (int, error) {
	sd := (*streamDecompressor)(sr)
	n := copy(p, sd.src[sd.srcOffset:])
	sd.srcOffset += n
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (sd *streamDecompressor) Write(p []byte) (int, error) {
	sd.dst = append(sd.dst, p...)
	return len(p), nil
}

func getStreamDecompressor(dd *DDict) *streamDecompressor {
	v := streamDecompressorPool.Get()
	if v == nil {
		sd := &streamDecompressor{
			zr: NewReader(nil),
		}
		v = sd
	}
	sd := v.(*streamDecompressor)
	sd.zr.Reset((*srcReader)(sd), dd)
	return sd
}

func putStreamDecompressor(sd *streamDecompressor) {
	sd.dst = nil
	sd.src = nil
	sd.srcOffset = 0
	sd.zr.Reset(nil, nil)
	streamDecompressorPool.Put(sd)
}

var streamDecompressorPool sync.Pool
