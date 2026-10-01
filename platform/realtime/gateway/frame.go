package gateway

type frameType string

const (
	frameConnectionInit     frameType = "connection_init"
	frameConnectionAck      frameType = "connection_ack"
	frameConnectionError    frameType = "connection_error"
	frameKeepAlive          frameType = "ka"
	frameSubscribe          frameType = "subscribe"
	frameSubscribeSuccess   frameType = "subscribe_success"
	frameSubscribeError     frameType = "subscribe_error"
	frameUnsubscribe        frameType = "unsubscribe"
	frameUnsubscribeSuccess frameType = "unsubscribe_success"
	frameUnsubscribeError   frameType = "unsubscribe_error"
	framePublish            frameType = "publish"
	framePublishError       frameType = "publish_error"
	frameData               frameType = "data"
	frameError              frameType = "error"
)

type errorType string

const (
	errorUnauthorized         errorType = "UnauthorizedException"
	errorBadRequest           errorType = "BadRequestException"
	errorLimitExceeded        errorType = "LimitExceededException"
	errorUnknownOperation     errorType = "UnknownOperationError"
	errorUnsupportedOperation errorType = "UnsupportedOperation"
)

type clientFrame struct {
	Type          frameType     `json:"type"`
	ID            string        `json:"id"`
	Channel       string        `json:"channel"`
	Authorization authorization `json:"authorization"`
}

type authorization struct {
	Token string `json:"Authorization"`
}

type ackFrame struct {
	Type                frameType `json:"type"`
	ConnectionTimeoutMs int64     `json:"connectionTimeoutMs"`
}

type idFrame struct {
	Type frameType `json:"type"`
	ID   string    `json:"id,omitempty"`
}

type errorFrame struct {
	Type   frameType      `json:"type,omitempty"`
	ID     string         `json:"id,omitempty"`
	Errors []errorMessage `json:"errors"`
}

type errorMessage struct {
	ErrorType errorType `json:"errorType"`
	Message   string    `json:"message"`
}

func newErrorFrame(of frameType, id string, kind errorType, message string) errorFrame {
	return errorFrame{Type: of, ID: id, Errors: []errorMessage{{ErrorType: kind, Message: message}}}
}

type dataFrame struct {
	Type  frameType `json:"type"`
	ID    string    `json:"id"`
	Event string    `json:"event"`
}
