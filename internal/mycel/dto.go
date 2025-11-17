package mycel

type cacheRequestType int

const (
	GET cacheRequestType = iota
)

type cacheRequest struct {
	requestType cacheRequestType
	key         string
	data        any
}
