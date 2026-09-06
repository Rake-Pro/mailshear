//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	exeSuffix            = ".exe"
	caseInsensitivePaths = true
)

// ensurePath appends dir to the per-user PATH in the registry (what the
// System Properties dialog edits) and broadcasts the change so newly started
// programs see it. Already-running programs, including an open IDE and its
// terminal tabs, keep their old environment until restarted.
func ensurePath(dir string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	cur, typ, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return err
	}
	for _, p := range strings.Split(cur, ";") {
		if samePath(p, dir) {
			if !onPath(dir) {
				fmt.Println("PATH already contains", dir, "for new programs; restart your terminal or IDE to pick it up")
			}
			return nil
		}
	}
	next := strings.TrimRight(cur, ";")
	if next != "" {
		next += ";"
	}
	next += dir
	if typ == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", next)
	} else {
		err = k.SetStringValue("Path", next)
	}
	if err != nil {
		return err
	}
	broadcastEnvironmentChange()
	fmt.Println("added", dir, "to the user PATH; restart your terminal or IDE to pick it up")
	return nil
}

func broadcastEnvironmentChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	env, _ := syscall.UTF16PtrFromString("Environment")
	var result uintptr
	proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}
