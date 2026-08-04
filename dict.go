package gozstd

/*
#cgo CFLAGS: -O3

#cgo noescape ZDICT_trainFromBuffer
#cgo nocallback ZDICT_trainFromBuffer
#cgo noescape ZSTD_createCDict
#cgo nocallback ZSTD_createCDict
#cgo noescape ZSTD_createDDict
#cgo nocallback ZSTD_createDDict

#define ZSTD_STATIC_LINKING_ONLY
#include "zstd.h"

#define ZDICT_STATIC_LINKING_ONLY
#include "zdict.h"

*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

const minDictLen = C.ZDICT_DICTSIZE_MIN

// BuildDict returns dictionary built from the given samples.
//
// The resulting dictionary size will be close to desiredDictLen.
//
// The returned dictionary may be passed to NewCDict* and NewDDict.
func BuildDict(samples [][]byte, desiredDictLen int) []byte {
	if desiredDictLen < minDictLen {
		desiredDictLen = minDictLen
	}
	dict := make([]byte, desiredDictLen)

	// Calculate the total samples size.
	samplesBufLen := 0
	for _, sample := range samples {
		if len(sample) == 0 {
			// Skip empty samples.
			continue
		}
		samplesBufLen += len(sample)
	}

	// Construct flat samplesBuf and samplesSizes.
	samplesBuf := make([]byte, 0, samplesBufLen)
	samplesSizes := make([]C.size_t, 0, len(samples))
	for _, sample := range samples {
		samplesBuf = append(samplesBuf, sample...)
		samplesSizes = append(samplesSizes, C.size_t(len(sample)))
	}

	// Add fake samples if the original samples are too small.
	minSamplesBufLen := int(C.ZDICT_CONTENTSIZE_MIN)
	if minSamplesBufLen < minDictLen {
		minSamplesBufLen = minDictLen
	}
	for samplesBufLen < minSamplesBufLen {
		fakeSample := []byte(fmt.Sprintf("this is a fake sample %d", samplesBufLen))
		samplesBuf = append(samplesBuf, fakeSample...)
		samplesSizes = append(samplesSizes, C.size_t(len(fakeSample)))
		samplesBufLen += len(fakeSample)
	}

	// Run ZDICT_trainFromBuffer under lock, since it looks like it
	// is unsafe for concurrent usage (it just randomly crashes).
	// TODO: remove this restriction.

	buildDictLock.Lock()
	result := C.ZDICT_trainFromBuffer(
		unsafe.Pointer(&dict[0]),
		C.size_t(len(dict)),
		unsafe.Pointer(&samplesBuf[0]),
		&samplesSizes[0],
		C.unsigned(len(samplesSizes)))
	buildDictLock.Unlock()
	if C.ZDICT_isError(result) != 0 {
		// Return empty dictionary, since the original samples are too small.
		return nil
	}

	dictLen := int(result)
	return dict[:dictLen]
}

var buildDictLock sync.Mutex

// CDict is a dictionary used for compression.
//
// A single CDict may be re-used in concurrently running goroutines.
type CDict struct {
	p                *C.ZSTD_CDict
	compressionLevel int
}

// NewCDict creates new CDict from the given dict.
//
// Call Release when the returned dict is no longer used.
func NewCDict(dict []byte) (*CDict, error) {
	return NewCDictLevel(dict, DefaultCompressionLevel)
}

// NewCDictLevel creates new CDict from the given dict
// using the given compressionLevel.
//
// Call Release when the returned dict is no longer used.
func NewCDictLevel(dict []byte, compressionLevel int) (*CDict, error) {
	if len(dict) == 0 {
		return nil, fmt.Errorf("dict cannot be empty")
	}

	cd := &CDict{
		p: C.ZSTD_createCDict(
			unsafe.Pointer(unsafe.SliceData(dict)),
			C.size_t(len(dict)),
			C.int(compressionLevel)),
		compressionLevel: compressionLevel,
	}
	runtime.SetFinalizer(cd, freeCDict)
	return cd, nil
}

// Release releases resources occupied by cd.
//
// cd cannot be used after the release.
func (cd *CDict) Release() {
	if cd.p == nil {
		return
	}
	result := C.ZSTD_freeCDict(cd.p)
	ensureNoError("ZSTD_freeCDict", result)
	cd.p = nil
}

func freeCDict(v interface{}) {
	v.(*CDict).Release()
}

// DDict is a dictionary used for decompression.
//
// A single DDict may be re-used in concurrently running goroutines.
type DDict struct {
	p *C.ZSTD_DDict
}

// NewDDict creates new DDict from the given dict.
//
// Call Release when the returned dict is no longer needed.
func NewDDict(dict []byte) (*DDict, error) {
	if len(dict) == 0 {
		return nil, fmt.Errorf("dict cannot be empty")
	}

	dd := &DDict{
		p: C.ZSTD_createDDict(
			unsafe.Pointer(unsafe.SliceData(dict)),
			C.size_t(len(dict))),
	}
	runtime.SetFinalizer(dd, freeDDict)
	return dd, nil
}

// Release releases resources occupied by dd.
//
// dd cannot be used after the release.
func (dd *DDict) Release() {
	if dd.p == nil {
		return
	}

	result := C.ZSTD_freeDDict(dd.p)
	ensureNoError("ZSTD_freeDDict", result)
	dd.p = nil
}

func freeDDict(v interface{}) {
	v.(*DDict).Release()
}
