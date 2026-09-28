package router

type Unserved struct{ Err error }

func (u Unserved) Error() string { return u.Err.Error() }

func (u Unserved) Unwrap() error { return u.Err }
