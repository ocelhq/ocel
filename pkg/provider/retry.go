package provider

import "time"

const (
	DefaultRetryMaxAttempts = 3
	DefaultRetryMinDelay    = time.Second
	DefaultRetryMaxDelay    = time.Minute
)
