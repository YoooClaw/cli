package calendar

// WithReadView coordinates snapshot acquisition with the existing writer.
// The callback must not call other Storage methods; close acquired handles itself.
func (s *Storage) WithReadView(read func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return read()
}
