package httpx

import (
	"errors"
	"fmt"
	"testing"
)

// TestWrapKeepsCodeAndCause 验证 Wrap 不修改预定义错误，且能拿到底层原因。
func TestWrapKeepsCodeAndCause(t *testing.T) {
	root := errors.New("connection reset")
	err := ErrInternal.Wrap(root)

	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatal("expected *Error in chain")
	}
	if appErr.Code != "INTERNAL" {
		t.Fatalf("code = %s, want INTERNAL", appErr.Code)
	}
	if !errors.Is(err, root) {
		t.Fatal("expected wrapped cause to be reachable via errors.Is")
	}
	// 关键：预定义错误本身不应被污染
	if ErrInternal.cause != nil {
		t.Fatal("shared ErrInternal was mutated by Wrap")
	}
}

// TestWithMessage 验证基于模板生成新错误。
func TestWithMessage(t *testing.T) {
	err := ErrNotFound.WithMessage("order %s not found", "GF2026")
	if got, want := err.Message, "order GF2026 not found"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if ErrNotFound.Message != "resource not found" {
		t.Fatal("shared ErrNotFound was mutated")
	}
}

// TestIsCode 验证错误码判断。
func TestIsCode(t *testing.T) {
	err := fmt.Errorf("service layer: %w", ErrConflict.Wrap(errors.New("version mismatch")))
	if !IsCode(err, "CONFLICT") {
		t.Fatal("expected CONFLICT code in error chain")
	}
	if IsCode(err, "NOT_FOUND") {
		t.Fatal("unexpected NOT_FOUND code")
	}
}
