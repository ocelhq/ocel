package infra

import ocel "github.com/ocelhq/ocel/sdk"

var DB = ocel.Postgres("main")

var Env = ocel.Env[struct {
	Greeting string `ocel:"GREETING,default=hello"`
}]()
