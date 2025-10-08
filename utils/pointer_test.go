// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Aditya Harindar <aditya.harindar@gmail.com>

package utils

import (
	"testing"
)

// TestPointer tests the generic Pointer function with various types
func TestPointer(t *testing.T) {
	t.Run("bool", func(t *testing.T) {
		val := true
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(true) returned nil")
		}
		if *ptr != val {
			t.Errorf("Pointer(true) = %v; want %v", *ptr, val)
		}
	})

	t.Run("string", func(t *testing.T) {
		val := "hello"
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(\"hello\") returned nil")
		}
		if *ptr != val {
			t.Errorf("Pointer(\"hello\") = %v; want %v", *ptr, val)
		}
	})

	t.Run("int", func(t *testing.T) {
		val := 42
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(42) returned nil")
		}
		if *ptr != val {
			t.Errorf("Pointer(42) = %v; want %v", *ptr, val)
		}
	})

	t.Run("int32", func(t *testing.T) {
		val := int32(42)
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(int32(42)) returned nil")
		}
		if *ptr != val {
			t.Errorf("Pointer(int32(42)) = %v; want %v", *ptr, val)
		}
	})

	t.Run("int64", func(t *testing.T) {
		val := int64(42)
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(int64(42)) returned nil")
		}
		if *ptr != val {
			t.Errorf("Pointer(int64(42)) = %v; want %v", *ptr, val)
		}
	})

	t.Run("struct", func(t *testing.T) {
		type testStruct struct {
			Name string
			Age  int
		}
		val := testStruct{Name: "Foo", Age: 1}
		ptr := Pointer(val)
		if ptr == nil {
			t.Fatal("Pointer(testStruct) returned nil")
		}
		if ptr.Name != val.Name || ptr.Age != val.Age {
			t.Errorf("Pointer(testStruct) = %v; want %v", *ptr, val)
		}
	})

	t.Run("zero values", func(t *testing.T) {
		// Test that zero values can be pointed to
		boolPtr := Pointer(false)
		if boolPtr == nil || *boolPtr != false {
			t.Errorf("Pointer(false) failed")
		}

		stringPtr := Pointer("")
		if stringPtr == nil || *stringPtr != "" {
			t.Errorf("Pointer(\"\") failed")
		}

		intPtr := Pointer(0)
		if intPtr == nil || *intPtr != 0 {
			t.Errorf("Pointer(0) failed")
		}
	})
}

// TestDereference tests the generic Dereference function with various types
func TestDereference(t *testing.T) {
	t.Run("bool non-nil", func(t *testing.T) {
		val := true
		ptr := &val
		result := Dereference(ptr)
		if result != val {
			t.Errorf("Dereference(&true) = %v; want %v", result, val)
		}
	})

	t.Run("bool nil", func(t *testing.T) {
		var ptr *bool
		result := Dereference(ptr)
		if result != false {
			t.Errorf("Dereference(nil *bool) = %v; want false", result)
		}
	})

	t.Run("string non-nil", func(t *testing.T) {
		val := "hello"
		ptr := &val
		result := Dereference(ptr)
		if result != val {
			t.Errorf("Dereference(&\"hello\") = %v; want %v", result, val)
		}
	})

	t.Run("string nil", func(t *testing.T) {
		var ptr *string
		result := Dereference(ptr)
		if result != "" {
			t.Errorf("Dereference(nil *string) = %v; want \"\"", result)
		}
	})

	t.Run("int non-nil", func(t *testing.T) {
		val := 42
		ptr := &val
		result := Dereference(ptr)
		if result != val {
			t.Errorf("Dereference(&42) = %v; want %v", result, val)
		}
	})

	t.Run("int nil", func(t *testing.T) {
		var ptr *int
		result := Dereference(ptr)
		if result != 0 {
			t.Errorf("Dereference(nil *int) = %v; want 0", result)
		}
	})

	t.Run("int32 non-nil", func(t *testing.T) {
		val := int32(42)
		ptr := &val
		result := Dereference(ptr)
		if result != val {
			t.Errorf("Dereference(&int32(42)) = %v; want %v", result, val)
		}
	})

	t.Run("int32 nil", func(t *testing.T) {
		var ptr *int32
		result := Dereference(ptr)
		if result != 0 {
			t.Errorf("Dereference(nil *int32) = %v; want 0", result)
		}
	})

	t.Run("int64 non-nil", func(t *testing.T) {
		val := int64(42)
		ptr := &val
		result := Dereference(ptr)
		if result != val {
			t.Errorf("Dereference(&int64(42)) = %v; want %v", result, val)
		}
	})

	t.Run("int64 nil", func(t *testing.T) {
		var ptr *int64
		result := Dereference(ptr)
		if result != 0 {
			t.Errorf("Dereference(nil *int64) = %v; want 0", result)
		}
	})

	t.Run("struct non-nil", func(t *testing.T) {
		type testStruct struct {
			Name string
			Age  int
		}
		val := testStruct{Name: "Alice", Age: 30}
		ptr := &val
		result := Dereference(ptr)
		if result.Name != val.Name || result.Age != val.Age {
			t.Errorf("Dereference(&testStruct) = %v; want %v", result, val)
		}
	})

	t.Run("struct nil", func(t *testing.T) {
		type testStruct struct {
			Name string
			Age  int
		}
		var ptr *testStruct
		result := Dereference(ptr)
		if result.Name != "" || result.Age != 0 {
			t.Errorf("Dereference(nil *testStruct) = %v; want zero value", result)
		}
	})
}

// TestPointerDereferenceRoundTrip tests that Pointer and Dereference work correctly together
func TestPointerDereferenceRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "bool",
			test: func(t *testing.T) {
				val := true
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
		{
			name: "string",
			test: func(t *testing.T) {
				val := "test"
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
		{
			name: "int",
			test: func(t *testing.T) {
				val := 123
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
		{
			name: "int32",
			test: func(t *testing.T) {
				val := int32(123)
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
		{
			name: "int64",
			test: func(t *testing.T) {
				val := int64(123)
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
		{
			name: "struct-nested",
			test: func(t *testing.T) {
				val := struct {
					A int
					B *struct {
						C string
					}
				}{A: 42, B: &struct{ C string }{C: "Foo"}}
				ptr := Pointer(val)
				result := Dereference(ptr)
				if result != val {
					t.Errorf("round trip failed: got %v, want %v", result, val)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.test)
	}
}
