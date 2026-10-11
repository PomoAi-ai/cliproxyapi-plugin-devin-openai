package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void *ptr; size_t len; } cliproxy_buffer;
typedef int (*host_call)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*buffer_free)(void*, size_t);
typedef struct { uint32_t abi_version; void *host_ctx; host_call call; buffer_free free_buffer; } cliproxy_host_api;
typedef int (*plugin_call)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef struct { uint32_t abi_version; plugin_call call; buffer_free free_buffer; void (*shutdown)(void); } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static cliproxy_host_api host_api;
static void init_api(cliproxy_host_api *host, cliproxy_plugin_api *plugin) {
 host_api = *host;
 plugin->abi_version = 1;
 plugin->call = cliproxyPluginCall;
 plugin->free_buffer = cliproxyPluginFree;
 plugin->shutdown = cliproxyPluginShutdown;
}
static int host_invoke(char *method, uint8_t *data, size_t len, cliproxy_buffer *out) {
 return host_api.call(host_api.host_ctx, method, data, len, out);
}
static void host_release(cliproxy_buffer out) { if(out.ptr) host_api.free_buffer(out.ptr, out.len); }
*/
import "C"

import (
	"encoding/json"
	"errors"
	"unsafe"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var instance = newPlugin(callHost)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 || host.call == nil || host.free_buffer == nil {
		return 1
	}
	C.init_api(host, plugin)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, out *C.cliproxy_buffer) C.int {
	if out == nil {
		return 1
	}
	out.ptr = nil
	out.len = 0
	if method == nil || size > 16<<20 || (size > 0 && request == nil) {
		return 1
	}
	result, err := instance.handle(C.GoString(method), C.GoBytes(unsafe.Pointer(request), C.int(size)))
	env := envelope{OK: err == nil}
	if err == nil {
		env.Result, err = json.Marshal(result)
	}
	if err != nil {
		env.OK = false
		env.Error = &rpcError{"devin_openai_error", err.Error()}
	}
	raw, _ := json.Marshal(env)
	out.ptr = C.CBytes(raw)
	out.len = C.size_t(len(raw))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) { C.free(ptr) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { instance.mu.Lock(); clear(instance.logins); instance.mu.Unlock() }

func callHost(method string, payload []byte) ([]byte, error) {
	name := C.CString(method)
	defer C.free(unsafe.Pointer(name))
	data := C.CBytes(payload)
	defer C.free(data)
	var out C.cliproxy_buffer
	status := C.host_invoke(name, (*C.uint8_t)(data), C.size_t(len(payload)), &out)
	defer C.host_release(out)
	if status != 0 || out.ptr == nil || out.len > 16<<20 {
		return nil, errors.New("host callback failed")
	}
	var env envelope
	if json.Unmarshal(C.GoBytes(out.ptr, C.int(out.len)), &env) != nil || !env.OK {
		return nil, errors.New("host callback rejected request")
	}
	return env.Result, nil
}
