// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package utils

// Pointer returns a pointer to the given value.
// Usage:
//
//	ptr := utils.Pointer(42) // *int pointing to 42
//	ptr := utils.Pointer("hello") // *string pointing to "hello"
func Pointer[T any](v T) *T {
	return &v
}

// Dereference returns the value of a pointer, or the zero value if the pointer is nil.
// Usage:
//
//	val := utils.Dereference(ptr) // returns value pointed by ptr, or zero value if ptr is nil
func Dereference[T any](p *T) T {
	var val T
	if p != nil {
		val = *p
	}
	return val
}

// Deprecated: Use Pointer instead.
// BoolP returns a pointer to the given bool value.
// Usage:
//
//	ptr := utils.BoolP(true) // *bool pointing to true
func BoolP(val bool) *bool {
	return &val
}

// Deprecated: Use Dereference instead.
// PBool returns the value of a *bool pointer, or false if the pointer is nil.
// Usage:
//
//	val := utils.PBool(ptr) // returns value pointed by ptr, or false if ptr is nil
func PBool(ptr *bool) bool {
	var val bool
	if ptr != nil {
		val = *ptr
	}
	return val
}

// Deprecated: Use Pointer instead.
// StringP returns a pointer to the given string value.
// Usage:
//
//	ptr := utils.StringP("hello") // *string pointing to "hello"
func StringP(val string) *string {
	return &val
}

// Deprecated: Use Dereference instead.
// PString returns the value of a *string pointer, or "" if the pointer is nil.
// Usage:
//
//	val := utils.PString(ptr) // returns value pointed by ptr, or "" if ptr is nil
func PString(ptr *string) string {
	var val string
	if ptr != nil {
		val = *ptr
	}
	return val
}

// Deprecated: Use Pointer instead.
// IntP returns a pointer to the given int value.
// Usage:
//
//	ptr := utils.IntP(42) // *int pointing to 42
func IntP(val int) *int {
	return &val
}

// Deprecated: Use Dereference instead.
// PInt returns the value of a *int pointer, or 0 if the pointer is nil.
// Usage:
//
//	val := utils.PInt(ptr) // returns value pointed by ptr, or 0 if ptr is nil
func PInt(ptr *int) int {
	var val int
	if ptr != nil {
		val = *ptr
	}
	return val
}

// Deprecated: Use Pointer instead.
// Int32P returns a pointer to the given int32 value.
// Usage:
//
//	ptr := utils.Int32P(42) // *int32 pointing to 42
func Int32P(val int32) *int32 {
	return &val
}

// Deprecated: Use Dereference instead.
// PInt32 returns the value of a *int32 pointer, or 0 if the pointer is nil.
// Usage:
//
//	val := utils.PInt32(ptr) // returns value pointed by ptr, or 0 if ptr is nil
func PInt32(ptr *int32) int32 {
	var val int32
	if ptr != nil {
		val = *ptr
	}
	return val
}

// Deprecated: Use Pointer instead.
// Int64P returns a pointer to the given int64 value.
// Usage:
//
//	ptr := utils.Int64P(42) // *int64 pointing to 42
func Int64P(val int64) *int64 {
	return &val
}

// Deprecated: Use Dereference instead.
// PInt64 returns the value of a *int64 pointer, or 0 if the pointer is nil.
// Usage:
//
//	val := utils.PInt64(ptr) // returns value pointed by ptr, or 0 if ptr is nil
func PInt64(ptr *int64) int64 {
	var val int64
	if ptr != nil {
		val = *ptr
	}
	return val
}
