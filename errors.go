package linkedin

import "errors"

var (
	ErrInvalidAuth   = errors.New("linkedin: invalid auth credentials")
	ErrUnauthorized  = errors.New("linkedin: unauthorized")
	ErrRateLimited   = errors.New("linkedin: rate limited")
	ErrNotFound      = errors.New("linkedin: not found")
	ErrInvalidParams = errors.New("linkedin: invalid parameters")
	ErrRequestFailed = errors.New("linkedin: request failed")
	ErrParseFailed   = errors.New("linkedin: failed to parse response")
)
