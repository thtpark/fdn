//go:build cgo && (darwin || ios)

package main

/*
#include <stdlib.h>
#include <stdint.h>
*/
import "C"

import (
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/mobile"
)

var handles = struct {
	sync.Mutex
	next    uint64
	id      uint64
	root    string
	host    *mobile.Runtime
	closing bool
}{}

func failure(err error) *C.char {
	data, marshalErr := json.Marshal(api.Envelope{Error: domain.AsFault(err), Warnings: []string{}})
	if marshalErr != nil {
		return C.CString(`{"ok":false,"warnings":[],"error":{"code":"unavailable","message":"native bridge encoding failed","retryable":false}}`)
	}
	return C.CString(string(data))
}

func input(data *C.char, length C.longlong) ([]byte, error) {
	if data == nil || length < 1 || length > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native input must be bounded UTF-8 bytes")
	}
	return C.GoBytes(unsafe.Pointer(data), C.int(length)), nil
}

func host(id C.ulonglong) (*mobile.Runtime, error) {
	handles.Lock()
	defer handles.Unlock()
	if handles.host == nil || handles.closing || uint64(id) != handles.id {
		return nil, domain.Fail(domain.Denied, "native runtime handle is closed or not owned by this app")
	}
	return handles.host, nil
}

//export FDNMobileOpen
func FDNMobileOpen(root *C.char, length C.longlong) *C.char {
	data, err := input(root, length)
	if err != nil {
		return failure(err)
	}
	handles.Lock()
	defer handles.Unlock()
	if handles.closing {
		return failure(domain.Fail(domain.Conflict, "the previous foreground runtime is still closing"))
	}
	if handles.host != nil {
		return failure(domain.Fail(domain.Conflict, "this app already owns its runtime; reuse that handle or close it explicitly first"))
	}
	if handles.host == nil {
		handles.host, err = mobile.New(string(data))
		if err != nil {
			return failure(err)
		}
		handles.next++
		handles.id, handles.root = handles.next, string(data)
	}
	result, err := json.Marshal(map[string]any{"ok": true, "warnings": []string{}, "data": map[string]any{"handle": handles.id}})
	if err != nil {
		return failure(err)
	}
	return C.CString(string(result))
}

//export FDNMobileCall
func FDNMobileCall(id C.ulonglong, data *C.char, length C.longlong) *C.char {
	runtime, err := host(id)
	if err != nil {
		return failure(err)
	}
	request, err := input(data, length)
	if err != nil {
		return failure(err)
	}
	return C.CString(string(runtime.Call(request)))
}

//export FDNMobileNext
func FDNMobileNext(id C.ulonglong) *C.char {
	runtime, err := host(id)
	if err != nil {
		return failure(err)
	}
	return C.CString(string(runtime.Next()))
}

//export FDNMobileCancel
func FDNMobileCancel(id C.ulonglong, data *C.char, length C.longlong) {
	runtime, err := host(id)
	if err != nil {
		return
	}
	request, err := input(data, length)
	if err != nil {
		return
	}
	runtime.Cancel(string(request))
}

//export FDNMobileClose
func FDNMobileClose(id C.ulonglong) *C.char {
	handles.Lock()
	if handles.host == nil || handles.closing || handles.id != uint64(id) {
		handles.Unlock()
		return failure(domain.Fail(domain.Denied, "native runtime handle is not active"))
	}
	runtime := handles.host
	handles.closing = true
	handles.Unlock()
	err := runtime.Close()
	handles.Lock()
	handles.host, handles.id, handles.root, handles.closing = nil, 0, "", false
	handles.Unlock()
	if err != nil {
		return failure(err)
	}
	return C.CString(`{"ok":true,"warnings":[],"data":{"closed":true}}`)
}

//export FDNMobileFree
func FDNMobileFree(data *C.char) { C.free(unsafe.Pointer(data)) }

func main() {}
