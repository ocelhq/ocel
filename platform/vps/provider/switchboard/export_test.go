package switchboard

import "net/http"

func (b *Board) DialConnectorAt(socket string) { b.connector = socket }

func (b *Board) Admit() http.Handler { return b.admit() }
