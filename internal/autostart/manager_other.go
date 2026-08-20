//go:build !darwin && !linux && !windows

package autostart

type unsupportedManager struct{ root string }

func newPlatformManager(root string) Manager   { return &unsupportedManager{root: root} }
func (m *unsupportedManager) Available() error { return ErrUnavailable }
func (m *unsupportedManager) Status() (Status, error) {
	return Status{Manager: "unsupported", Unit: ServiceID(m.root)}, nil
}
func (m *unsupportedManager) Install(Spec) error { return ErrUnavailable }
func (m *unsupportedManager) Start() error       { return ErrUnavailable }
func (m *unsupportedManager) Stop() error        { return nil }
func (m *unsupportedManager) Restart() error     { return ErrUnavailable }
func (m *unsupportedManager) Uninstall() error   { return nil }
