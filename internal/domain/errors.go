// Package domain contains the core business entities and logic.
package domain

import "errors"

// Sentinel errors for common domain error cases.
// These allow handlers to check error types without coupling to infrastructure.
var (
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = errors.New("resource not found")

	// ErrAlreadyExists indicates a resource with the same identifier already exists.
	ErrAlreadyExists = errors.New("resource already exists")

	// ErrInvalidInput indicates the input data is invalid or malformed.
	ErrInvalidInput = errors.New("invalid input")
)
