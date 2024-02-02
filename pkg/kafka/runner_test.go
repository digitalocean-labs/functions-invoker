package kafka

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/rs/zerolog"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"

	"invoker/pkg/entities"
	kafkamocks "invoker/pkg/kafka/mocks"
	"invoker/pkg/protocol"
)

func TestRunnerInvoke(t *testing.T) {
	t.Parallel()

	// Activations to be "returned" by the inner runner.
	successfulActivation := &entities.Activation{
		Response: entities.ActivationResponse{
			StatusCode: entities.ActivationStatusCodeSuccess,
		},
	}
	successfulActivationBs, err := json.Marshal(successfulActivation)
	require.NoError(t, err)

	tests := []struct {
		name              string
		prep              func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner)
		blockedNamespaces map[string]struct{}
		input             *protocol.ActivationMessage
	}{{
		name: "successful async invocation",
		input: &protocol.ActivationMessage{
			ActivationID:        "testactivationid",
			TransactionID:       "testtransactionid",
			TransactionStart:    time.UnixMilli(1337), // Very old but still gets executed.
			RootControllerIndex: "53",
			IsBlocking:          false,
		},
		prep: func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner) {
			innerRunner.EXPECT().Invoke(gomock.Any(), gomock.Any(), msg).Return(successfulActivation)
			kafkaWriter.EXPECT().WriteMessages(gomock.Any(), kafka.Message{
				Topic: "completed53",
				Value: protocol.MiniCombinedCompletionMessage(42, msg.TransactionID, msg.TransactionStart, msg.ActivationID, false /*isSystemError*/),
			})
		},
	}, {
		name: "successful blocking invocation",
		input: &protocol.ActivationMessage{
			ActivationID:        "testactivationid",
			TransactionID:       "testtransactionid",
			TransactionStart:    time.Now(),
			RootControllerIndex: "53",
			IsBlocking:          true,
		},
		prep: func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner) {
			innerRunner.EXPECT().Invoke(gomock.Any(), gomock.Any(), msg).Return(successfulActivation)
			kafkaWriter.EXPECT().WriteMessages(gomock.Any(), kafka.Message{
				Topic: "completed53",
				Value: protocol.CombinedCompletionMessage(42, msg.TransactionID, msg.TransactionStart, successfulActivationBs, false /*isSystemError*/),
			})
		},
	}, {
		name: "system error",
		input: &protocol.ActivationMessage{
			ActivationID:        "testactivationid",
			TransactionID:       "testtransactionid",
			TransactionStart:    time.UnixMilli(1337),
			RootControllerIndex: "53",
		},
		prep: func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner) {
			innerRunner.EXPECT().Invoke(gomock.Any(), gomock.Any(), msg).Return(&entities.Activation{
				Response: entities.ActivationResponse{
					StatusCode: entities.ActivationStatusCodeSystemError,
				},
			})
			kafkaWriter.EXPECT().WriteMessages(gomock.Any(), kafka.Message{
				Topic: "completed53",
				// We expect the isSystemError flag to be set.
				Value: protocol.MiniCombinedCompletionMessage(42, msg.TransactionID, msg.TransactionStart, msg.ActivationID, true /*isSystemError*/),
			})
		},
	}, {
		name: "blocked namespace",
		input: &protocol.ActivationMessage{
			ActivationID:        "testactivationid",
			TransactionID:       "testtransactionid",
			TransactionStart:    time.UnixMilli(1337),
			RootControllerIndex: "53",
			InvocationIdentity: protocol.InvocationIdentity{
				Namespace: "blockednamespace",
			},
		},
		blockedNamespaces: map[string]struct{}{
			"blockednamespace": {},
		},
		prep: func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner) {
			// We don't expect the inner runner to be called here at all!

			// We do expect a response message to be written to unblock the controller though.
			kafkaWriter.EXPECT().WriteMessages(gomock.Any(), kafka.Message{
				Topic: "completed53",
				Value: protocol.MiniCombinedCompletionMessage(42, msg.TransactionID, msg.TransactionStart, msg.ActivationID, false /*isSystemError*/),
			})
		},
	}, {
		name: "old invocation request",
		input: &protocol.ActivationMessage{
			ActivationID:        "testactivationid",
			TransactionID:       "testtransactionid",
			TransactionStart:    time.UnixMilli(1337), // Very old.
			IsBlocking:          true,
			RootControllerIndex: "53",
		},
		prep: func(msg *protocol.ActivationMessage, kafkaWriter *kafkamocks.MockWriter, innerRunner *kafkamocks.MockInnerRunner) {
			// We don't expect the inner runner to be called here at all!

			// We do expect a response message to be written to unblock the controller though.
			kafkaWriter.EXPECT().WriteMessages(gomock.Any(), kafka.Message{
				Topic: "completed53",
				Value: protocol.MiniCombinedCompletionMessage(42, msg.TransactionID, msg.TransactionStart, msg.ActivationID, false /*isSystemError*/),
			})
		},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup all required mocks.
			ctrl := gomock.NewController(t)
			kafkaWriter := kafkamocks.NewMockWriter(ctrl)
			innerRunner := kafkamocks.NewMockInnerRunner(ctrl)

			tt.prep(tt.input, kafkaWriter, innerRunner)

			runner := &Runner{
				InstanceID:        42,
				KafkaWriter:       kafkaWriter,
				InnerRunner:       innerRunner,
				BlockedNamespaces: tt.blockedNamespaces,
			}
			runner.Invoke(context.Background(), zerolog.New(zerolog.NewTestWriter(t)), tt.input)
		})
	}
}
