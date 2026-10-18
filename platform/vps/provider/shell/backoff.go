package shell

import (
	"math/rand/v2"
	"strconv"
	"time"
)

type Backoff struct {
	Counter string
	Base    int
	Ceiling int
	Spread  int
}

func (h Backoff) After(try int) time.Duration {
	backoff := min(h.Base<<(try-1), h.Ceiling)
	return time.Duration(backoff+rand.IntN(h.Spread)) * time.Second
}

func (h Backoff) Start() string { return "backoff=" + strconv.Itoa(h.Base) + "\n" }

func (h Backoff) Again() string {
	return `jitter=$(awk -v seed=$$ -v n="$` + h.Counter + `" -v spread=` + strconv.Itoa(h.Spread) + ` 'BEGIN{srand(seed+n);print int(rand()*spread)}' 2>/dev/null || echo 0)
[ -n "$jitter" ] || jitter=0
sleep $((backoff + jitter))
backoff=$((backoff * 2))
if [ "$backoff" -gt ` + strconv.Itoa(h.Ceiling) + ` ]; then backoff=` + strconv.Itoa(h.Ceiling) + `; fi
`
}
