package infra

import (
	"ocel.dev/bucket"
	"ocel.dev/env"
	"ocel.dev/postgres"
)

var DB = postgres.New("main")

var Env = env.Define[struct {
	Greeting     string     `ocel:"GREETING,default=hello"`
	JourneyNonce env.Secret `ocel:"JOURNEY_NONCE"`
}]()

var Uploads = bucket.New("uploads")
