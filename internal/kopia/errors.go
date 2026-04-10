package kopia

import "fmt"

// ServerNotReadyError indicates the Kopia server is not ready yet.
type ServerNotReadyError struct {
	Message string
	Err     error
}

func (e *ServerNotReadyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *ServerNotReadyError) Unwrap() error {
	return e.Err
}
