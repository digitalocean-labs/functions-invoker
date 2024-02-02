// Code generated by MockGen. DO NOT EDIT.
// Source: runner.go

// Package mocks is a generated GoMock package.
package mocks

import (
	context "context"
	entities "invoker/pkg/entities"
	protocol "invoker/pkg/protocol"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
	zerolog "github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
)

// MockWriter is a mock of Writer interface.
type MockWriter struct {
	ctrl     *gomock.Controller
	recorder *MockWriterMockRecorder
}

// MockWriterMockRecorder is the mock recorder for MockWriter.
type MockWriterMockRecorder struct {
	mock *MockWriter
}

// NewMockWriter creates a new mock instance.
func NewMockWriter(ctrl *gomock.Controller) *MockWriter {
	mock := &MockWriter{ctrl: ctrl}
	mock.recorder = &MockWriterMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockWriter) EXPECT() *MockWriterMockRecorder {
	return m.recorder
}

// WriteMessages mocks base method.
func (m *MockWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	m.ctrl.T.Helper()
	varargs := []interface{}{ctx}
	for _, a := range msgs {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "WriteMessages", varargs...)
	ret0, _ := ret[0].(error)
	return ret0
}

// WriteMessages indicates an expected call of WriteMessages.
func (mr *MockWriterMockRecorder) WriteMessages(ctx interface{}, msgs ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]interface{}{ctx}, msgs...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WriteMessages", reflect.TypeOf((*MockWriter)(nil).WriteMessages), varargs...)
}

// MockInnerRunner is a mock of InnerRunner interface.
type MockInnerRunner struct {
	ctrl     *gomock.Controller
	recorder *MockInnerRunnerMockRecorder
}

// MockInnerRunnerMockRecorder is the mock recorder for MockInnerRunner.
type MockInnerRunnerMockRecorder struct {
	mock *MockInnerRunner
}

// NewMockInnerRunner creates a new mock instance.
func NewMockInnerRunner(ctrl *gomock.Controller) *MockInnerRunner {
	mock := &MockInnerRunner{ctrl: ctrl}
	mock.recorder = &MockInnerRunnerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockInnerRunner) EXPECT() *MockInnerRunnerMockRecorder {
	return m.recorder
}

// Invoke mocks base method.
func (m *MockInnerRunner) Invoke(ctx context.Context, logger zerolog.Logger, msg *protocol.ActivationMessage) *entities.Activation {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Invoke", ctx, logger, msg)
	ret0, _ := ret[0].(*entities.Activation)
	return ret0
}

// Invoke indicates an expected call of Invoke.
func (mr *MockInnerRunnerMockRecorder) Invoke(ctx, logger, msg interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Invoke", reflect.TypeOf((*MockInnerRunner)(nil).Invoke), ctx, logger, msg)
}