package providerhost

import "time"

func WithQuestionWait(config Config, wait time.Duration) Config {
	config.questionWait = wait
	return config
}
