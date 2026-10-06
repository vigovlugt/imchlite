package onnxruntime

// The onnxruntime Go binding has no API for plugin execution providers, so
// this file calls the C API directly. The runtime's environment is a
// process-wide singleton, so CreateEnv here returns the one the binding
// created, and the plugin registered on it is visible to the binding's
// sessions. Appending the plugin to session options needs the binding's
// unexported OrtSessionOptions handle; see sessionOptionsHandle.

/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo linux LDFLAGS: -ldl
#include <stdlib.h>
#include "onnxruntime_c_api.h"

#ifdef _WIN32
#include <windows.h>
#else
#include <dlfcn.h>
#endif

static const OrtApi *api;
static OrtEnv *env;

// plugin_init looks up the already loaded runtime library at lib and takes a
// reference to its environment.
static const char *plugin_init(const void *lib) {
	void *get_api_base;
#ifdef _WIN32
	HMODULE h = GetModuleHandleW((const wchar_t *)lib);
	if (h == NULL) return "onnxruntime library is not loaded";
	get_api_base = (void *)GetProcAddress(h, "OrtGetApiBase");
#else
	void *h = dlopen((const char *)lib, RTLD_NOW | RTLD_NOLOAD);
	if (h == NULL) return "onnxruntime library is not loaded";
	get_api_base = dlsym(h, "OrtGetApiBase");
	dlclose(h);
#endif
	if (get_api_base == NULL) return "OrtGetApiBase not found";
	api = ((const OrtApiBase *(*)(void))get_api_base)()->GetApi(ORT_API_VERSION);
	if (api == NULL) return "onnxruntime library is older than the plugin api headers";
	return NULL;
}

// status_message returns the message of status, which may be NULL, and
// releases it. The message is copied to C memory the caller frees.
static char *status_message(OrtStatus *status) {
	if (status == NULL) return NULL;
	char *msg = strdup(api->GetErrorMessage(status));
	api->ReleaseStatus(status);
	return msg;
}

static char *create_env(void) {
	return status_message(api->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "imchlite", &env));
}

static void release_env(void) {
	api->ReleaseEnv(env);
	env = NULL;
}

static char *register_library(const char *name, const void *path) {
	return status_message(api->RegisterExecutionProviderLibrary(env, name, (const ORTCHAR_T *)path));
}

static char *unregister_library(const char *name) {
	return status_message(api->UnregisterExecutionProviderLibrary(env, name));
}

// find_device returns the first device of the execution provider named
// ep_name of the given hardware type, or NULL.
static const OrtEpDevice *find_device(const char *ep_name, OrtHardwareDeviceType type, char **err) {
	const OrtEpDevice *const *devices;
	size_t n;
	*err = status_message(api->GetEpDevices(env, &devices, &n));
	if (*err != NULL) return NULL;
	for (size_t i = 0; i < n; i++) {
		if (strcmp(api->EpDevice_EpName(devices[i]), ep_name) == 0 &&
		    api->HardwareDevice_Type(api->EpDevice_Device(devices[i])) == type) {
			return devices[i];
		}
	}
	return NULL;
}

static uint32_t device_vendor_id(const OrtEpDevice *d) {
	return api->HardwareDevice_VendorId(api->EpDevice_Device(d));
}

static uint32_t device_id(const OrtEpDevice *d) {
	return api->HardwareDevice_DeviceId(api->EpDevice_Device(d));
}

static char *add_config_entry(void *opts, const char *key, const char *value) {
	return status_message(api->AddSessionConfigEntry((OrtSessionOptions *)opts, key, value));
}

static char *append_device(void *opts, const OrtEpDevice *device) {
	return status_message(api->SessionOptionsAppendExecutionProvider_V2(
		(OrtSessionOptions *)opts, env, &device, 1, NULL, NULL, 0));
}
*/
import "C"

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
)

const webGPURegistrationName = "WebGPU"

// cError converts a message returned by the C helpers to an error, freeing it.
func cError(msg *C.char) error {
	if msg == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(msg))
	return errors.New(C.GoString(msg))
}

// gpuDevice is a GPU an execution provider runs on.
type gpuDevice struct {
	handle   *C.OrtEpDevice
	vendorID uint32
	deviceID uint32
}

// registerWebGPU registers the WebGPU plugin library at pluginPath with the
// runtime loaded from libPath, which ort.Init must have loaded, and returns
// the GPU it runs on.
func registerWebGPU(libPath, pluginPath string) (*gpuDevice, error) {
	lib, freeLib, err := cPath(libPath)
	if err != nil {
		return nil, err
	}
	defer freeLib()
	if msg := C.plugin_init(lib); msg != nil {
		return nil, errors.New(C.GoString(msg))
	}
	if err := cError(C.create_env()); err != nil {
		return nil, fmt.Errorf("create env: %w", err)
	}

	plugin, freePlugin, err := cPath(pluginPath)
	if err != nil {
		return nil, err
	}
	defer freePlugin()
	name := C.CString(webGPURegistrationName)
	defer C.free(unsafe.Pointer(name))
	if err := cError(C.register_library(name, plugin)); err != nil {
		return nil, fmt.Errorf("register webgpu plugin: %w", err)
	}

	epName := C.CString(webGPUEpName)
	defer C.free(unsafe.Pointer(epName))
	var msg *C.char
	d := C.find_device(epName, C.OrtHardwareDeviceType_GPU, &msg)
	if err := cError(msg); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	if d == nil {
		return nil, errors.New("no gpu found")
	}
	return &gpuDevice{
		handle:   d,
		vendorID: uint32(C.device_vendor_id(d)),
		deviceID: uint32(C.device_id(d)),
	}, nil
}

// unregisterWebGPU unregisters the plugin and releases the environment
// reference taken by registerWebGPU. Every session must be closed first.
func unregisterWebGPU() error {
	name := C.CString(webGPURegistrationName)
	defer C.free(unsafe.Pointer(name))
	err := cError(C.unregister_library(name))
	C.release_env()
	return err
}

// appendDevice makes sessions created with opts run on d.
func appendDevice(opts *ort.SessionOptions, d *gpuDevice) error {
	handle, err := sessionOptionsHandle(opts)
	if err != nil {
		return err
	}
	return cError(C.append_device(handle, d.handle))
}

// handleCheckKey is a session config entry set through the raw handle and
// read back through the binding, to prove the handle belongs to opts.
const handleCheckKey = "imchlite.session_options_handle_check"

// sessionOptionsHandle returns the OrtSessionOptions handle of opts. The
// binding keeps it in an unexported field, so it is read through unsafe,
// which depends on the binding's private struct layout. The layout is
// checked first and the handle verified after, so a binding update that
// changes it returns an error instead of passing garbage to the C API.
func sessionOptionsHandle(opts *ort.SessionOptions) (unsafe.Pointer, error) {
	field := reflect.TypeFor[ort.SessionOptions]().Field(0)
	if field.Name != "handle" || field.Offset != 0 || field.Type.Kind() != reflect.Pointer ||
		field.Type.Elem().Name() != "_Ctype_struct_OrtSessionOptions" {
		return nil, fmt.Errorf("unexpected ort.SessionOptions layout: first field is %s %s", field.Name, field.Type)
	}

	handle := *(*unsafe.Pointer)(unsafe.Pointer(opts))
	if handle == nil {
		return nil, errors.New("session options are closed")
	}

	key, value := C.CString(handleCheckKey), C.CString("1")
	defer C.free(unsafe.Pointer(key))
	defer C.free(unsafe.Pointer(value))
	if err := cError(C.add_config_entry(handle, key, value)); err != nil {
		return nil, fmt.Errorf("check session options handle: %w", err)
	}
	if got, err := opts.GetSessionConfigEntry(handleCheckKey); err != nil || got != "1" {
		return nil, fmt.Errorf("session options handle check failed (got %q, %v)", got, err)
	}
	return handle, nil
}
