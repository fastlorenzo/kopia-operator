package kopia

// ServerNotReadyError indicates the Kopia server is not ready yet.
type ServerNotReadyError struct {
	Message string
}

func (e *ServerNotReadyError) Error() string {
	return e.Message
}
