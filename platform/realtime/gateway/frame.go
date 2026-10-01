package gateway

const (
	frameConnectionInit     = "connection_init"
	frameConnectionAck      = "connection_ack"
	frameConnectionError    = "connection_error"
	frameKeepAlive          = "ka"
	frameSubscribe          = "subscribe"
	frameSubscribeSuccess   = "subscribe_success"
	frameSubscribeError     = "subscribe_error"
	frameUnsubscribe        = "unsubscribe"
	frameUnsubscribeSuccess = "unsubscribe_success"
	frameUnsubscribeError   = "unsubscribe_error"
	framePublish            = "publish"
	framePublishError       = "publish_error"
	frameData               = "data"
	frameError              = "error"
)

type clientFrame struct {
	Type          string        `json:"type"`
	ID            string        `json:"id"`
	Channel       string        `json:"channel"`
	Authorization authorization `json:"authorization"`
}

type authorization struct {
	Host  string `json:"host,omitempty"`
	Token string `json:"Authorization"`
}

type ackFrame struct {
	Type                string `json:"type"`
	ConnectionTimeoutMs int64  `json:"connectionTimeoutMs"`
}

type idFrame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type errorFrame struct {
	Type   string         `json:"type"`
	ID     string         `json:"id,omitempty"`
	Errors []errorMessage `json:"errors"`
}

type errorMessage struct {
	ErrorType string `json:"errorType"`
	Message   string `json:"message"`
}

type dataFrame struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Event string `json:"event"`
}

const (
	errorUnauthorized  = "UnauthorizedException"
	errorBadRequest    = "BadRequestException"
	errorLimitExceeded = "LimitExceededException"
	errorUnknownOp     = "UnknownOperationError"
	errorUnsupportedOp = "UnsupportedOperation"
)
