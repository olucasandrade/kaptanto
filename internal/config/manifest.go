package config

// ValidOutputModes returns the supported --output values in stable order.
// Used by docs parity tests and CLI help text guards.
func ValidOutputModes() []string {
	return []string{
		"none",
		"stdout",
		"sse",
		"grpc",
		"nats",
		"sqs",
		"kafka",
		"pubsub",
		"rabbitmq",
		"webhook",
		"vector",
	}
}

// ValidSinkKeys returns the YAML keys under Config.Sinks that map to sink blocks.
func ValidSinkKeys() []string {
	return []string{
		"kafka",
		"nats",
		"sqs",
		"pubsub",
		"rabbitmq",
		"webhook",
		"vector",
	}
}
