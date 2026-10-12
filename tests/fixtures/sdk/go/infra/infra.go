package infra

import "ocel.dev"

var DB = ocel.Postgres("main")

var Env = ocel.Env[struct {
	Greeting     string      `ocel:"GREETING,default=hello"`
	JourneyNonce ocel.Secret `ocel:"JOURNEY_NONCE"`
}]()

var Uploads = ocel.Bucket("uploads")
