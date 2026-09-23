//go:build windows

package scoopupgrade

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func processStarted(pid int) uint64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return 0
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
}
func processAlive(pid int, started uint64) bool {
	if pid <= 0 || started == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != started {
		return false
	}
	code, err := windows.WaitForSingleObject(h, 0)
	return err == nil && code == uint32(windows.WAIT_TIMEOUT)
}
func secureDirectory(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func lockState(root string) (func(), error) {
	runtime.LockOSThread()
	acquired := false
	defer func() {
		if !acquired {
			runtime.UnlockOSThread()
		}
	}()
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	name, err := windows.UTF16PtrFromString(fmt.Sprintf("Local\\PersonalCLIUpgrade-%x", key))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, err
	}
	code, err := windows.WaitForSingleObject(h, 5000)
	if err != nil || (code != windows.WAIT_OBJECT_0 && code != windows.WAIT_ABANDONED) {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("another upgrade is preparing; retry later")
	}
	acquired = true
	return func() { _ = windows.ReleaseMutex(h); _ = windows.CloseHandle(h); runtime.UnlockOSThread() }, nil
}
func configureHelper(cmd *exec.Cmd, interactive bool) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB)
	if interactive {
		flags |= windows.CREATE_NEW_CONSOLE
	} else {
		flags |= windows.CREATE_NO_WINDOW
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}
func configureManager(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	// Terminate only our own manager process tree on cancellation. This never
	// stops other application instances; those remain Scoop's blocking policy.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		system := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
		kill := exec.Command(system, "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
func installedProcessRunning(root, app string) bool {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(snapshot)
	prefix := strings.ToLower(filepath.Join(root, "apps", app)) + string(filepath.Separator)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if e != nil {
			continue
		}
		buffer := make([]uint16, 32768)
		length := uint32(len(buffer))
		e = windows.QueryFullProcessImageName(h, 0, &buffer[0], &length)
		windows.CloseHandle(h)
		if e == nil && strings.HasPrefix(strings.ToLower(windows.UTF16ToString(buffer[:length])), prefix) {
			return true
		}
	}
	return false
}
func helperConsole(interactive bool, log io.Writer) (io.Writer, func()) {
	if !interactive {
		return log, func() {}
	}
	console, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return log, func() {}
	}
	return io.MultiWriter(log, console), func() { console.Close() }
}
func consolePause(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	console, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err == nil {
		fmt.Fprintln(console, "Press Enter to close this window.")
		console.Close()
	}
	in, err := os.Open("CONIN$")
	if err != nil {
		return
	}
	defer in.Close()
	done := make(chan struct{})
	go func() { var b [2]byte; _, _ = in.Read(b[:]); close(done) }()
	select {
	case <-ctx.Done():
	case <-done:
	}
}
