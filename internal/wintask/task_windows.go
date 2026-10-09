//go:build windows

package wintask

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/YoooClaw/cli/internal/taskrepair"
)

type Scheduler struct{ service, folder, task *ole.IDispatch }

func dispatch(v *ole.VARIANT, err error) (*ole.IDispatch, error) {
	if err != nil {
		if v != nil {
			v.Clear()
		}
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("Task Scheduler 返回空 VARIANT")
	}
	d := v.ToIDispatch()
	if d == nil {
		v.Clear()
		return nil, fmt.Errorf("Task Scheduler 返回无效对象")
	}
	d.AddRef()
	v.Clear()
	return d, nil
}
func text(v *ole.VARIANT, err error) (string, error) {
	if err != nil {
		if v != nil {
			v.Clear()
		}
		return "", err
	}
	if v == nil {
		return "", fmt.Errorf("Task Scheduler 返回空 VARIANT")
	}
	defer v.Clear()
	return v.ToString(), nil
}
func call(d *ole.IDispatch, name string, args ...interface{}) error {
	v, err := oleutil.CallMethod(d, name, args...)
	if v != nil {
		v.Clear()
	}
	if err != nil {
		return &taskrepair.StepError{Step: name, Err: err}
	}
	return nil
}

// Call invokes an operation while preserving the native HRESULT chain.
func Call(d *ole.IDispatch, name string, args ...interface{}) error { return call(d, name, args...) }
func Open(traces ...func(string, ...any)) (*Scheduler, error) {
	trace := func(string, ...any) {}
	if len(traces) > 0 {
		trace = traces[0]
	}
	step := func(name string, action func() error) error {
		trace("BEGIN %s", name)
		if err := action(); err != nil {
			wrapped := &taskrepair.StepError{Step: name, Err: err}
			trace("FAILED %s", wrapped)
			return wrapped
		}
		trace("OK %s", name)
		return nil
	}
	runtime.LockOSThread()
	if err := step("CoInitializeEx", func() error {
		err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
		if e, ok := err.(*ole.OleError); ok && e.Code() == 1 {
			return nil
		} // S_FALSE is success.
		return err
	}); err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	s := &Scheduler{}
	var u *ole.IUnknown
	err := step("CreateObject", func() error { var e error; u, e = oleutil.CreateObject("Schedule.Service"); return e })
	if err != nil {
		s.Close()
		return nil, err
	}
	err = step("QueryInterface", func() error { var e error; s.service, e = u.QueryInterface(ole.IID_IDispatch); return e })
	u.Release()
	if err == nil {
		err = step("Connect", func() error { return connectLocal(s.service) })
	}
	if err == nil {
		err = step("GetFolder", func() error {
			var e error
			s.folder, e = dispatch(oleutil.CallMethod(s.service, "GetFolder", `\YoooClaw`))
			return e
		})
	}
	if err == nil {
		err = step("GetTask", s.Refresh)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("读取现有 YoooClaw 任务失败（不会当作任务不存在）：%w", err)
	}
	return s, nil
}
func (s *Scheduler) Close() {
	if s.task != nil {
		s.task.Release()
	}
	if s.folder != nil {
		s.folder.Release()
	}
	if s.service != nil {
		s.service.Release()
	}
	ole.CoUninitialize()
	runtime.UnlockOSThread()
}
func (s *Scheduler) Refresh() error {
	if s.task != nil {
		s.task.Release()
		s.task = nil
	}
	var err error
	s.task, err = dispatch(oleutil.CallMethod(s.folder, "GetTask", "yoooclaw-daemon"))
	if err != nil {
		return &taskrepair.StepError{Step: "GetTask", Err: err}
	}
	return nil
}
func (s *Scheduler) XML() (string, error) {
	v, err := text(oleutil.GetProperty(s.task, "Xml"))
	if err != nil {
		return "", &taskrepair.StepError{Step: "GetProperty(Xml)", Err: err}
	}
	return v, nil
}
func (s *Scheduler) SD() (string, error) {
	v, err := text(oleutil.CallMethod(s.task, "GetSecurityDescriptor", 7))
	if err != nil {
		return "", &taskrepair.StepError{Step: "GetSecurityDescriptor", Err: err}
	}
	return v, nil
}
func (s *Scheduler) Running() (bool, error) {
	if err := s.Refresh(); err != nil {
		return false, err
	}
	v, err := oleutil.GetProperty(s.task, "State")
	if err != nil {
		return false, &taskrepair.StepError{Step: "GetProperty(State)", Err: err}
	}
	defer v.Clear()
	return v.Val == 4, nil
}
func (s *Scheduler) Enabled() (bool, error) {
	v, err := oleutil.GetProperty(s.task, "Enabled")
	if err != nil {
		return false, &taskrepair.StepError{Step: "GetProperty(Enabled)", Err: err}
	}
	defer v.Clear()
	return v.Val != 0, nil
}

func ResolveSID(value string) (string, error) {
	if strings.HasPrefix(value, "S-1-") {
		sid, err := windows.StringToSid(value)
		if err != nil {
			return "", err
		}
		return sid.String(), nil
	}
	sid, _, _, err := windows.LookupSID("", value)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}
func CurrentSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
func ProfileFor(sid string) (string, error) {
	if !strings.HasPrefix(sid, "S-1-5-21-") {
		return "", fmt.Errorf("不支持的用户 SID")
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return "", err
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+sid, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	p, _, err := k.GetStringValue("ProfileImagePath")
	// No expansion in another administrator's context.
	if strings.Contains(p, "%") || len(p) < 4 || p[1] != ':' {
		return "", fmt.Errorf("用户 profile 路径需要人工核实")
	}
	return strings.TrimRight(p, `\`), err
}

// GrantTask touches ONLY the verified existing task DACL. Never folder ACLs,
// files under System32/Tasks, owner fields, or the task's running identity.
func GrantTask(sid string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("未获得管理员授权")
	}
	p, err := ProfileFor(sid)
	if err != nil {
		return err
	}
	s, err := Open()
	if err != nil {
		return err
	}
	defer s.Close()
	raw, err := s.XML()
	if err != nil {
		return err
	}
	if err = taskrepair.Validate(raw, sid, p, ResolveSID); err != nil {
		return err
	}
	before, err := s.SD()
	if err != nil {
		return err
	}
	desc, err := windows.SecurityDescriptorFromString(before)
	if err != nil {
		return err
	}
	old, _, err := desc.DACL()
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("任务 DACL 异常，停止修改")
	}
	user, err := windows.StringToSid(sid)
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user)}}}, old)
	if err != nil {
		return err
	}
	runtime.KeepAlive(user)
	daclOnly, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err = daclOnly.SetDACL(acl, true, false); err != nil {
		return err
	}
	control, _, err := desc.Control()
	if err != nil {
		return err
	}
	const flags = windows.SE_DACL_PROTECTED | windows.SE_DACL_AUTO_INHERITED
	if err = daclOnly.SetControl(flags, control&flags); err != nil {
		return err
	}
	// TASK_DONT_ADD_PRINCIPAL_ACE: do not silently add the elevated account.
	if err = call(s.task, "SetSecurityDescriptor", daclOnly.String(), 16); err != nil {
		return fmt.Errorf("任务权限修复失败：%w", err)
	}
	return nil
}

// Service returns the native automation interface; callers must keep the session open.
func (s *Scheduler) Service() *ole.IDispatch { return s.service }
func (s *Scheduler) Folder() *ole.IDispatch  { return s.folder }
func (s *Scheduler) Task() *ole.IDispatch    { return s.task }
