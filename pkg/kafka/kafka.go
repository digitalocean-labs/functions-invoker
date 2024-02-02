package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"

	"invoker/pkg/units"
)

const (
	connectionTimeout = 10 * time.Second

	// invokerMaxMessageSize is the largest message size expected via the invoker kafka topic.
	// We usually allow for at most 1 megabyte of payload in and out, so this value is way higher
	// than that to avoid blocking messages in kafka unnecessarily.
	invokerMaxMessageSize = 2 * units.Megabyte

	// tlsServerName is a "fake" server name to use with a *tls.Config to make Kafka accept the
	// certs as-is. They're all wildcard certs, so the first part of the name can be anything really.
	tlsServerName = "kafka.openwhisk.svc.cluster.local"
)

// NewKafkaWriter returns a *kafka.Writer configured to talk to the given host with the given
// *tls.Config. Internal batching is disabled to allow for the lowest latency possible.
func NewKafkaWriter(host string, tlsConfig *tls.Config) *kafka.Writer {
	kafkaTLSConfig := tlsConfig.Clone()
	kafkaTLSConfig.ServerName = tlsServerName

	return &kafka.Writer{
		Addr: kafka.TCP(host),
		Transport: &kafka.Transport{
			Dial: (&net.Dialer{
				Timeout: connectionTimeout,
			}).DialContext,
			TLS: kafkaTLSConfig,
		},
		BatchBytes: int64(invokerMaxMessageSize),
		BatchSize:  1, // Disable batching.
		// Increase the default max attempts of 3 to paper over issues more aggressively.
		MaxAttempts: 10,
	}
}

// NewKafkaReader returns a *kafka.Reader configured to talk to the given host with the given
// *tls.Config and consume the given topic. Read settings (min/max bytes and max wait) are adjusted
// to offer the lowest latency possible.
func NewKafkaReader(host, topic string, tlsConfig *tls.Config) *kafka.Reader {
	kafkaTLSConfig := tlsConfig.Clone()
	kafkaTLSConfig.ServerName = tlsServerName
	dialer := &kafka.Dialer{
		Timeout: connectionTimeout,
		TLS:     kafkaTLSConfig,
	}

	return kafka.NewReader(kafka.ReaderConfig{
		Dialer:   dialer,
		Brokers:  []string{host},
		GroupID:  topic,
		Topic:    topic,
		MinBytes: 1,
		MaxBytes: int(invokerMaxMessageSize),
		MaxWait:  500 * time.Millisecond,
		// Increase the default max attempts of 3 to paper over issues more aggressively.
		MaxAttempts: 10,
		// If there's no committed offset (or if it has vanished due to retention) pick up the topic
		// from the last offset. This is a safety measure to avoid double execution of functions.
		// Worst-case, the old Kafka version can't deal with the new retention time we're setting below
		// and we'd potentially lose offsets after not executing anything for 24h (the default in pre
		// 2.0 Kafka brokers). Arguably, we wouldn't want to execute 24h old invocations anyway,
		// so the risk of actually losing valuable data here should be very low.
		StartOffset: kafka.LastOffset,
		// Increase the time the offsets are kept in Kafka to 7 days (the default in Kafka 2.0+). As
		// long as this is higher than the retention on the invoker topics (see below) we'd never
		// lose material offsets.
		// Since we now invoke the majority of our functions through HTTP, it's very possible to reach
		// this retention for clusters that are never overloaded.
		RetentionTime: 7 * 24 * time.Hour,
	})
}

// NewKafkaClient returns a *kafka.Client configured to talk to the given host with the given
// *tls.Config.
func NewKafkaClient(host string, tlsConfig *tls.Config) *kafka.Client {
	kafkaTLSConfig := tlsConfig.Clone()
	kafkaTLSConfig.ServerName = tlsServerName
	return &kafka.Client{
		Addr: kafka.TCP(host),
		Transport: &kafka.Transport{
			Dial: (&net.Dialer{
				Timeout: connectionTimeout,
			}).DialContext,
			TLS: kafkaTLSConfig,
		},
	}
}

// CreateInvokerTopic creates the topic that the invoker uses to listen for its work.
func CreateInvokerTopic(host string, tlsConfig *tls.Config, topic string) error {
	if err := CreateTopic(host, tlsConfig, kafka.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
		ConfigEntries: []kafka.ConfigEntry{{
			ConfigName:  "max.message.bytes",
			ConfigValue: strconv.Itoa(int(invokerMaxMessageSize)),
		}, {
			ConfigName:  "segment.bytes",
			ConfigValue: strconv.Itoa(int(512 * units.Megabyte)),
		}, {
			ConfigName:  "retention.bytes",
			ConfigValue: strconv.Itoa(int(1 * units.Gigabyte)),
		}, {
			ConfigName:  "retention.ms",
			ConfigValue: strconv.Itoa(int((48 * time.Hour).Milliseconds())),
		}},
	}); err != nil {
		return fmt.Errorf("failed to create invoker topic: %w", err)
	}
	return nil
}

// Create topic creates a topic with the given config.
func CreateTopic(host string, tlsConfig *tls.Config, topic kafka.TopicConfig) error {
	kafkaTLSConfig := tlsConfig.Clone()
	kafkaTLSConfig.ServerName = tlsServerName
	dialer := &kafka.Dialer{
		Timeout: connectionTimeout,
		TLS:     kafkaTLSConfig,
	}

	conn, err := dialer.Dial("tcp", host)
	if err != nil {
		return fmt.Errorf("failed to connect to kafka to create topic: %w", err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("failed to get controller of the kafka cluster: %w", err)
	}
	var controllerConn *kafka.Conn
	controllerConn, err = dialer.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return fmt.Errorf("failed to connect to controller to create topic: %w", err)
	}
	defer controllerConn.Close()

	if err := controllerConn.CreateTopics(topic); err != nil {
		return fmt.Errorf("failed to create topic: %w", err)
	}
	return nil
}

// FetchLastOffset fetches the last offset of the given group and topic.
// It is assumed that we're only dealing with one partition and thus it's safe to return just one
// value.
func FetchLastOffset(client *kafka.Client, topic string) (int64, error) {
	offsets, err := client.ListOffsets(context.Background(), &kafka.ListOffsetsRequest{
		Topics: map[string][]kafka.OffsetRequest{
			topic: {kafka.LastOffsetOf(0)},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to read offsets from kafka: %w", err)
	}
	partitions, ok := offsets.Topics[topic]
	if !ok {
		return 0, fmt.Errorf("offsets from kafka didn't contain %q", topic)
	}
	if len(partitions) == 0 {
		return 0, errors.New("offsets didn't contain expected partition 0")
	}
	return partitions[0].LastOffset, nil
}
