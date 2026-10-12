package infra

import "ocel.dev"

var DB = ocel.Postgres("main")

var Env = ocel.Env[struct {
	Greeting string `ocel:"GREETING,default=hello"`
}]()

var Uploads = ocel.Bucket("uploads")
