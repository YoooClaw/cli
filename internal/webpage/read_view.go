package webpage

// WithReadView holds the writer lock while the asset reader reads the index.
// The callback must not call any mutating webpage operation.
func WithReadView(read func() error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return read()
}
