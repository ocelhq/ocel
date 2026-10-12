package mountedgate

import "time"

func (s *Source) SetReadTimeout(timeout time.Duration) { s.timeout = timeout }
