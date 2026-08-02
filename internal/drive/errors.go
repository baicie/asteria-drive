package drive

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	CodeInvalidRequest        ErrorCode = "invalid_request"
	CodeInvalidCursor         ErrorCode = "invalid_cursor"
	CodeUnauthenticated       ErrorCode = "unauthenticated"
	CodeNotFound              ErrorCode = "not_found"
	CodeNameConflict          ErrorCode = "name_conflict"
	CodeInvalidState          ErrorCode = "invalid_state"
	CodeIdempotencyConflict   ErrorCode = "idempotency_conflict"
	CodeRestoreConflict       ErrorCode = "restore_conflict"
	CodeRevisionMismatch      ErrorCode = "revision_mismatch"
	CodeRequestTooLarge       ErrorCode = "request_too_large"
	CodeUnsupportedMediaType  ErrorCode = "unsupported_media_type"
	CodeDependencyUnavailable ErrorCode = "dependency_unavailable"
	CodeInternal              ErrorCode = "internal_error"
)

type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return string(e.Code) + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func E(code ErrorCode, message string, cause ...error) error {
	var c error
	if len(cause) > 0 {
		c = cause[0]
	}
	return &Error{Code: code, Message: message, Cause: c}
}

func Retryable(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Retryable: true, Cause: cause}
}

func CodeOf(err error) ErrorCode {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	return CodeInternal
}
