package infra

import (
	"ocel.dev/bucket"
	"ocel.dev/env"
	"ocel.dev/postgres"
)

var DB = postgres.New("main")

var Env = env.MustDefine[struct {
	Greeting string `ocel:"GREETING,default=hello"`
}]()

var Uploads = bucket.New("uploads")
