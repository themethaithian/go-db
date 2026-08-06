//go:build darwin

package app

/*
#include <mach/mach.h>

// phys_footprint reads the current process's physical footprint via mach
// TASK_VM_INFO — the exact number Activity Monitor's Memory column shows
// for this process. Returns 0 on failure so the Go side can fall back.
static unsigned long long phys_footprint(void) {
	task_vm_info_data_t info;
	mach_msg_type_number_t count = TASK_VM_INFO_COUNT;
	kern_return_t kr = task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&info, &count);
	if (kr != KERN_SUCCESS) {
		return 0;
	}
	return info.phys_footprint;
}
*/
import "C"

// memoryUsageBytes reads the app process's physical footprint in bytes via
// mach task_info(TASK_VM_INFO) — this is what's measured: the app process
// itself, nothing else. WKWebView helper processes launched by the OS to
// render the frontend are separate processes with their own footprint and
// are not included here; README's "Measured" section covers the whole
// picture by adding them in separately. Returns ok=false if the mach call
// fails, so the caller can fall back to a Go-level estimate.
func memoryUsageBytes() (bytes uint64, ok bool) {
	v := uint64(C.phys_footprint())
	if v == 0 {
		return 0, false
	}
	return v, true
}
